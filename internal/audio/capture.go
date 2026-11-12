package audio

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Recorder defaults.
const (
	// DefaultPreRoll is how much audio from before Start an always-on
	// microphone keeps, so the first syllable is not lost.
	DefaultPreRoll = 200 * time.Millisecond
	// DefaultCloseDelay is how long the microphone stays open after Stop
	// when it is not always on, so a quick next recording does not wait for
	// the device again.
	DefaultCloseDelay = time.Second
)

const (
	// pumpInterval is how often the samples move from the device's ring
	// buffer to the recording: the granularity of Frames.
	pumpInterval = 10 * time.Millisecond
	// ringSeconds of audio fit in the ring buffer, which bounds how long the
	// pump may stall before samples are dropped.
	ringSeconds = 2
)

var (
	// ErrRecording means Start was called during a recording.
	ErrRecording = errors.New("audio: already recording")
	// ErrClosed means the Recorder was closed.
	ErrClosed = errors.New("audio: recorder closed")
)

// Options configure a Recorder.
type Options struct {
	// Device is the microphone's name or ID; "" means the system default.
	// When it is not connected, the Recorder uses the default and says so
	// through Notify.
	Device string
	// AlwaysOn keeps the microphone open between recordings, so a recording
	// starts at once and begins with PreRoll of the audio before Start. Off,
	// the microphone opens at Start and closes CloseDelay after Stop.
	AlwaysOn bool
	// PreRoll is how much audio an always-on microphone keeps from before
	// Start. Zero means DefaultPreRoll.
	PreRoll time.Duration
	// CloseDelay is how long the microphone stays open after Stop when it is
	// not always on. Zero means DefaultCloseDelay; negative closes it at once.
	CloseDelay time.Duration
	// Notify, if set, receives messages for the user, such as a fallback to
	// the default microphone. It must not block.
	Notify func(msg string)
}

// Recorder records from a microphone: mono float32 samples at the device's
// native rate, which the engine resamples itself. The device callback only
// copies samples into a lock-free ring buffer; a goroutine moves them into
// the recording, Frames and Levels. A Recorder is safe for concurrent use.
type Recorder struct {
	be     backend
	levels chan Level

	mu         sync.Mutex
	opts       Options
	closed     bool
	in         *openDevice // the open microphone, or nil
	sess       *session    // the recording in progress, or nil
	pre        *history    // pre-roll, while always on
	closeTimer *time.Timer
	meter      *meter
	rate       int    // of the current or latest recording
	dropped    uint64 // by microphones closed before the current one
}

// session is one recording.
type session struct {
	samples []float32
	frames  chan []float32 // nil until Frames is called
	notify  chan struct{}  // wakes the Frames forwarder
	queued  int            // samples handed to the forwarder
	tail    []float32      // what the forwarder still has to send after Stop
	done    bool
}

// openDevice is an open microphone and the goroutine draining it.
type openDevice struct {
	stream  inputStream
	dev     Device
	rate    int
	ring    atomic.Pointer[ring]
	closing atomic.Bool
	lost    chan struct{} // signalled by the device's stop callback
	flush   chan chan struct{}
	stop    chan struct{}
	done    chan struct{}
}

// NewRecorder returns a Recorder. It opens no device: Start does, or Open for
// an always-on microphone.
func NewRecorder(opts Options) *Recorder {
	return newRecorder(systemBackend(), opts)
}

func newRecorder(be backend, opts Options) *Recorder {
	return &Recorder{be: be, opts: opts, levels: make(chan Level, 1)}
}

// Open opens the microphone now rather than at the next Start. An always-on
// Recorder starts keeping pre-roll from here.
func (r *Recorder) Open() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	if r.in != nil {
		return nil
	}
	return r.openLocked()
}

// Start begins a recording, opening the microphone if it is not open.
func (r *Recorder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.closed:
		return ErrClosed
	case r.sess != nil:
		return ErrRecording
	}
	if r.closeTimer != nil {
		r.closeTimer.Stop()
		r.closeTimer = nil
	}
	if r.in == nil {
		if err := r.openLocked(); err != nil {
			return err
		}
	}
	s := &session{notify: make(chan struct{}, 1)}
	if r.pre != nil {
		s.samples = r.pre.take()
	}
	r.sess, r.rate, r.meter = s, r.in.rate, newMeter(r.in.rate)
	return nil
}

// Stop ends the recording and returns its samples, at SampleRate, with the
// pre-roll first. It returns nil when not recording. The caller owns the
// slice.
func (r *Recorder) Stop() []float32 {
	r.mu.Lock()
	s, d := r.sess, r.in
	r.mu.Unlock()
	if s == nil {
		return nil
	}
	if d != nil {
		d.flushNow() // what the device delivered up to now
	}

	r.mu.Lock()
	if r.sess != s {
		r.mu.Unlock()
		return nil // a concurrent Stop or Close got there first
	}
	r.endLocked(s)
	var retired *openDevice
	if d := r.in; d != nil {
		switch {
		case d.closing.Load(): // SetDevice switched microphones during the recording
			retired = r.detachLocked()
			if r.opts.AlwaysOn {
				if err := r.openLocked(); err != nil {
					r.notifyf("The microphone could not be reopened: %v", err)
				}
			}
		case !r.opts.AlwaysOn:
			retired = r.scheduleCloseLocked()
		}
	}
	r.mu.Unlock()
	r.closeDevice(retired)
	return s.samples
}

func (r *Recorder) endLocked(s *session) {
	r.sess = nil
	s.done = true
	if s.frames != nil {
		s.tail = append([]float32(nil), s.samples[s.queued:]...)
	}
	wake(s.notify)
}

// Frames returns the recording's audio as it arrives, in chunks of about
// 10 ms (larger when the reader falls behind), starting with the pre-roll.
// The channel closes after Stop has delivered every sample, so the chunks
// add up to exactly what Stop returns. Read it until it closes. Outside a
// recording it returns a closed channel.
func (r *Recorder) Frames() <-chan []float32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sess
	if s == nil {
		ch := make(chan []float32)
		close(ch)
		return ch
	}
	if s.frames == nil {
		s.frames = make(chan []float32, 64)
		go r.forward(s)
	}
	return s.frames
}

// forward sends a session's samples to its Frames channel. It never holds
// up the pump: it copies what is new and sends it outside the lock.
func (r *Recorder) forward(s *session) {
	defer close(s.frames)
	for {
		r.mu.Lock()
		var chunk []float32
		if !s.done {
			if len(s.samples) > s.queued {
				chunk = append(chunk, s.samples[s.queued:]...)
				s.queued = len(s.samples)
			}
		} else {
			chunk, s.tail = s.tail, nil
		}
		done := s.done
		r.mu.Unlock()
		switch {
		case len(chunk) > 0:
			s.frames <- chunk
		case done:
			return
		default:
			<-s.notify
		}
	}
}

// Levels delivers the recording's level about LevelRate times a second, for a
// meter. A slow reader gets the latest level, not a backlog. The channel
// lives as long as the Recorder.
func (r *Recorder) Levels() <-chan Level { return r.levels }

// SampleRate returns the rate of the current or latest recording, in Hz, or 0
// before the first one.
func (r *Recorder) SampleRate() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rate
}

// Device returns the open microphone, or false when none is open.
func (r *Recorder) Device() (Device, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.in == nil {
		return Device{}, false
	}
	return r.in.dev, true
}

// Dropped returns how many samples were lost because the ring buffer was
// full, over the Recorder's life. It stays 0 unless the process stalls for
// about two seconds.
func (r *Recorder) Dropped() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.dropped
	if r.in != nil {
		if q := r.in.ring.Load(); q != nil {
			n += q.dropped.Load()
		}
	}
	return n
}

// SetDevice switches microphones. The open one, if any, closes at once when
// idle, or after the recording in progress.
func (r *Recorder) SetDevice(device string) {
	r.mu.Lock()
	if r.opts.Device == device {
		r.mu.Unlock()
		return
	}
	r.opts.Device = device
	d := r.reopenLocked()
	r.mu.Unlock()
	r.closeDevice(d)
}

// SetAlwaysOn turns the always-on microphone on or off. Turning it on opens
// the microphone; turning it off closes it when idle.
func (r *Recorder) SetAlwaysOn(on bool) error {
	r.mu.Lock()
	if r.opts.AlwaysOn == on {
		r.mu.Unlock()
		return nil
	}
	r.opts.AlwaysOn = on
	var d *openDevice
	if r.in != nil {
		if on {
			r.pre = newHistory(r.preRollSamples(r.in.rate))
		} else {
			r.pre = nil
			if r.sess == nil {
				d = r.detachLocked()
			}
		}
	}
	r.mu.Unlock()
	r.closeDevice(d)
	if on {
		return r.Open()
	}
	return nil
}

// reopenLocked arranges for the next Start to open the device afresh: it
// detaches the open device when idle and returns it for closing, or marks it
// to close when the recording ends.
func (r *Recorder) reopenLocked() *openDevice {
	if r.in == nil {
		return nil
	}
	if r.sess != nil {
		r.in.closing.Store(true) // Stop closes it
		return nil
	}
	d := r.detachLocked()
	if r.opts.AlwaysOn {
		if err := r.openLocked(); err != nil {
			r.notifyf("The microphone could not be reopened: %v", err)
		}
	}
	return d
}

// Close ends any recording and closes the microphone.
func (r *Recorder) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	if r.closeTimer != nil {
		r.closeTimer.Stop()
		r.closeTimer = nil
	}
	if s := r.sess; s != nil {
		r.endLocked(s)
	}
	d := r.detachLocked()
	r.mu.Unlock()
	r.closeDevice(d)
}

func (r *Recorder) preRollSamples(rate int) int {
	pre := r.opts.PreRoll
	if pre <= 0 {
		pre = DefaultPreRoll
	}
	return int(int64(rate) * int64(pre) / int64(time.Second))
}

func (r *Recorder) notifyf(format string, args ...any) {
	if r.opts.Notify != nil {
		r.opts.Notify(fmt.Sprintf(format, args...))
	}
}

// resolve picks the device to open: the configured one if it is connected,
// otherwise the default, with a notice. It also returns the default, for a
// fallback. A failure to list the devices is not reported as a missing
// device, since it may be passing.
func (r *Recorder) resolve() (pick, def Device) {
	devs, err := r.be.devices(captureDevices)
	def = Device{Name: "default microphone", Default: true}
	for _, d := range devs {
		if d.Default {
			def = d
		}
	}
	want := r.opts.Device
	if want == "" || err != nil {
		return def, def
	}
	if d, ok := findDevice(devs, want); ok {
		return d, def
	}
	r.notifyf("The microphone %q is not connected; using the default microphone%s.", want, nameSuffix(def))
	return def, def
}

func nameSuffix(d Device) string {
	if d.Name == "" {
		return ""
	}
	return fmt.Sprintf(" (%s)", d.Name)
}

// openLocked opens the microphone and starts the pump that drains it. The
// default microphone is opened by asking the system for its default rather
// than by ID, so it follows the system's choice.
func (r *Recorder) openLocked() error {
	dev, def := r.resolve()
	d := &openDevice{
		dev:   dev,
		lost:  make(chan struct{}, 1),
		flush: make(chan chan struct{}),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	onData := func(p []float32) {
		if q := d.ring.Load(); q != nil {
			q.write(p)
		}
	}
	onStop := func() {
		if !d.closing.Load() {
			wake(d.lost)
		}
	}
	openID := dev.ID
	if dev.Default {
		openID = ""
	}
	st, err := r.be.open(openID, onData, onStop)
	if err != nil && openID != "" {
		r.notifyf("The microphone %q could not be opened (%v); using the default microphone%s.", dev.Name, err, nameSuffix(def))
		d.dev = def
		st, err = r.be.open("", onData, onStop)
	}
	if err != nil {
		return fmt.Errorf("audio: open microphone: %w", err)
	}
	d.stream, d.rate = st, st.rate()
	d.ring.Store(newRing(d.rate * ringSeconds))
	if err := st.start(); err != nil {
		d.closing.Store(true)
		st.close()
		return fmt.Errorf("audio: start microphone %q: %w", d.dev.Name, err)
	}
	r.in = d
	if r.opts.AlwaysOn {
		r.pre = newHistory(r.preRollSamples(d.rate))
	}
	go r.pump(d)
	return nil
}

// scheduleCloseLocked closes the idle microphone after the close delay. With
// a negative delay it detaches it at once and returns it for closeDevice.
func (r *Recorder) scheduleCloseLocked() *openDevice {
	delay := r.opts.CloseDelay
	if delay == 0 {
		delay = DefaultCloseDelay
	}
	if delay < 0 {
		return r.detachLocked()
	}
	d := r.in
	r.closeTimer = time.AfterFunc(delay, func() {
		r.mu.Lock()
		if r.in != d || r.sess != nil || r.opts.AlwaysOn {
			r.mu.Unlock()
			return
		}
		r.detachLocked()
		r.mu.Unlock()
		r.closeDevice(d)
	})
	return nil
}

// detachLocked takes the open device out of service and returns it for
// closeDevice, which must be called without r.mu held.
func (r *Recorder) detachLocked() *openDevice {
	d := r.in
	if d == nil {
		return nil
	}
	r.in, r.pre = nil, nil
	d.closing.Store(true)
	if q := d.ring.Load(); q != nil {
		r.dropped += q.dropped.Load()
	}
	return d
}

// closeDevice stops a detached device and its pump.
func (r *Recorder) closeDevice(d *openDevice) {
	if d == nil {
		return
	}
	d.stream.close()
	close(d.stop)
	<-d.done
}

// pump moves samples from a device's ring buffer into the recording, the
// pre-roll and the level meter, until the device closes or is lost.
func (r *Recorder) pump(d *openDevice) {
	defer close(d.done)
	t := time.NewTicker(pumpInterval)
	defer t.Stop()
	buf := make([]float32, d.rate*ringSeconds)
	for {
		select {
		case <-t.C:
			r.drain(d, buf)
		case ack := <-d.flush:
			r.drain(d, buf)
			close(ack)
		case <-d.lost:
			r.drain(d, buf)
			r.lose(d)
		case <-d.stop:
			return
		}
	}
}

// flushNow has the pump drain the ring buffer and waits for it.
func (d *openDevice) flushNow() {
	ack := make(chan struct{})
	select {
	case d.flush <- ack:
		<-ack
	case <-d.done:
	}
}

func (r *Recorder) drain(d *openDevice, buf []float32) {
	q := d.ring.Load()
	for {
		n := q.read(buf)
		if n == 0 {
			return
		}
		p := buf[:n]
		r.mu.Lock()
		if r.in == d {
			switch s := r.sess; {
			case s != nil && d.rate == r.rate:
				s.samples = append(s.samples, p...)
				wake(s.notify)
				r.meter.add(p, r.sendLevel)
			case s == nil && r.pre != nil:
				r.pre.add(p)
			}
		}
		r.mu.Unlock()
	}
}

// sendLevel replaces any level the reader has not taken yet. Only the pump
// sends, with r.mu held.
func (r *Recorder) sendLevel(l Level) {
	select {
	case r.levels <- l:
	default:
		select {
		case <-r.levels:
		default:
		}
		select {
		case r.levels <- l:
		default:
		}
	}
}

// lose handles a microphone that stopped on its own, such as one that was
// unplugged. It reopens a microphone, the default one if the configured one
// is gone, when recording or always on. A recording continues on it when
// the sample rate is the same; otherwise it keeps what it has, and Stop
// returns that.
func (r *Recorder) lose(d *openDevice) {
	r.mu.Lock()
	if r.in != d {
		r.mu.Unlock()
		return
	}
	r.detachLocked()
	r.notifyf("The microphone %q stopped (disconnected?).", d.dev.Name)
	if !r.closed && (r.sess != nil || r.opts.AlwaysOn) {
		if err := r.openLocked(); err != nil {
			r.notifyf("No microphone could be opened: %v", err)
		} else if r.sess != nil && r.in.rate != r.rate {
			r.notifyf("The rest of this recording is lost: %q runs at %d Hz, not %d Hz.", r.in.dev.Name, r.in.rate, r.rate)
		}
	}
	r.mu.Unlock()
	// The pump is this goroutine: release the device without waiting for it.
	d.stream.close()
	close(d.stop)
}

func wake(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
