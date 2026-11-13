package audio

import (
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackend is a set of synthetic microphones. Tests feed samples into an
// open one as its device callback would.
type fakeBackend struct {
	mu      sync.Mutex
	devs    []Device
	listErr error
	rates   map[string]int   // by ID; "" is the default device
	openErr map[string]error // by ID; "out:" and an ID for playback
	inputs  []*fakeInput
	outputs []*fakeOutputStream
}

type fakeInput struct {
	id     string
	hz     int
	onData func([]float32)
	onStop func()

	mu      sync.Mutex
	started bool
	closed  bool
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		devs: []Device{
			{ID: "aa01", Name: "Built-in Microphone", Default: true},
			{ID: "bb02", Name: "USB Headset"},
		},
		rates:   map[string]int{"": 48000, "aa01": 48000, "bb02": 16000},
		openErr: map[string]error{},
	}
}

func (b *fakeBackend) devices(deviceKind) ([]Device, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.devs), b.listErr
}

func (b *fakeBackend) open(id string, onData func([]float32), onStop func()) (inputStream, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.openErr[id]; err != nil {
		return nil, err
	}
	in := &fakeInput{id: id, hz: b.rates[id], onData: onData, onStop: onStop}
	b.inputs = append(b.inputs, in)
	return in, nil
}

func (b *fakeBackend) opened() []*fakeInput {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.inputs)
}

func (b *fakeBackend) last(t *testing.T) *fakeInput {
	t.Helper()
	ins := b.opened()
	if len(ins) == 0 {
		t.Fatal("no device opened")
	}
	return ins[len(ins)-1]
}

func (f *fakeInput) rate() int { return f.hz }

func (f *fakeInput) start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
	return nil
}

func (f *fakeInput) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func (f *fakeInput) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// feed delivers samples as the device callback would, in 10 ms periods.
func (f *fakeInput) feed(p []float32) {
	f.mu.Lock()
	live := f.started && !f.closed
	f.mu.Unlock()
	if !live {
		return
	}
	period := max(1, f.hz/100)
	for len(p) > 0 {
		n := min(period, len(p))
		f.onData(p[:n])
		p = p[n:]
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

type notices struct {
	mu   sync.Mutex
	msgs []string
}

func (n *notices) add(m string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, m)
}

func (n *notices) all() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return strings.Join(n.msgs, "\n")
}

func TestRecordStartStop(t *testing.T) {
	be := newFakeBackend()
	r := newRecorder(be, Options{})
	defer r.Close()
	if got := r.Stop(); got != nil {
		t.Errorf("Stop before Start = %v", got)
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(); !errors.Is(err, ErrRecording) {
		t.Errorf("second Start: %v", err)
	}
	in := be.last(t)
	if in.id != "" {
		t.Errorf("opened %q; the default device opens by asking for the default", in.id)
	}
	if d, ok := r.Device(); !ok || d.Name != "Built-in Microphone" || d.ID != "aa01" {
		t.Errorf("Device() = %+v, %t", d, ok)
	}
	in.feed(ramp(0, 4800))
	time.Sleep(15 * time.Millisecond) // let the pump run once
	in.feed(ramp(4800, 2000))
	got := r.Stop() // flushes what is still in the ring
	if !slices.Equal(got, ramp(0, 6800)) {
		t.Fatalf("recorded %d samples, want the 6800 fed in order", len(got))
	}
	if r.SampleRate() != 48000 || r.Dropped() != 0 {
		t.Errorf("rate %d, dropped %d", r.SampleRate(), r.Dropped())
	}
}

func TestFramesAddUpToTheRecording(t *testing.T) {
	be := newFakeBackend()
	r := newRecorder(be, Options{CloseDelay: -1})
	defer r.Close()
	if ch := r.Frames(); ch == nil {
		t.Fatal("nil channel outside a recording")
	} else if _, open := <-ch; open {
		t.Error("Frames outside a recording is not closed")
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	frames := r.Frames()
	if r.Frames() != frames {
		t.Error("Frames returned a different channel within one recording")
	}
	var streamed []float32
	chunks := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for f := range frames {
			streamed = append(streamed, f...)
			chunks++
		}
	}()
	in := be.last(t)
	for i := range 30 {
		in.feed(ramp(i*480, 480))
		time.Sleep(2 * time.Millisecond)
	}
	got := r.Stop()
	<-done
	if !slices.Equal(streamed, got) || len(got) != 30*480 {
		t.Errorf("Frames gave %d samples, Stop %d; want the same 14400", len(streamed), len(got))
	}
	if chunks < 2 {
		t.Errorf("%d chunks: frames did not stream", chunks)
	}
	if !in.isClosed() {
		t.Error("CloseDelay -1 left the device open")
	}
}

func TestLevelsWhileRecording(t *testing.T) {
	be := newFakeBackend()
	r := newRecorder(be, Options{})
	defer r.Close()
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	be.last(t).feed(sine(48000, 48000, 0.5, 440))
	r.Stop()
	select {
	case l := <-r.Levels():
		if math.Abs(float64(l.RMS)-0.354) > 0.02 || math.Abs(float64(l.Peak)-0.5) > 0.01 {
			t.Errorf("level %+v", l)
		}
	default:
		t.Fatal("no level")
	}
	select {
	case l := <-r.Levels():
		t.Errorf("a backlog of levels: %+v", l)
	default:
	}
}

func TestAlwaysOnKeepsPreRoll(t *testing.T) {
	be := newFakeBackend()
	r := newRecorder(be, Options{Device: "USB Headset", AlwaysOn: true, PreRoll: 100 * time.Millisecond})
	defer r.Close()
	if err := r.Open(); err != nil {
		t.Fatal(err)
	}
	in := be.last(t)
	if in.id != "bb02" || in.hz != 16000 {
		t.Fatalf("opened %q at %d Hz", in.id, in.hz)
	}
	in.feed(ramp(0, 5000)) // before Start: only the last 1600 stay
	eventually(t, "the pump to drain", func() bool { return r.in.ring.Load().buffered() == 0 })
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	in.feed(ramp(5000, 1000))
	got := r.Stop()
	if !slices.Equal(got, ramp(3400, 2600)) {
		t.Errorf("recorded %d samples from %v, want 100 ms of pre-roll then the recording", len(got), got[:1])
	}
	time.Sleep(20 * time.Millisecond)
	if in.isClosed() || len(be.opened()) != 1 {
		t.Error("an always-on microphone closed after Stop")
	}

	// The next recording starts with the audio since this one.
	in.feed(ramp(10, 50))
	eventually(t, "the pump to drain", func() bool { return r.in.ring.Load().buffered() == 0 })
	_ = r.Start()
	if got := r.Stop(); !slices.Equal(got, ramp(10, 50)) {
		t.Errorf("second recording %v", got)
	}

	if err := r.SetAlwaysOn(false); err != nil {
		t.Fatal(err)
	}
	if !in.isClosed() {
		t.Error("turning always-on off left the idle microphone open")
	}
}

func TestMicrophoneClosesShortlyAfterStop(t *testing.T) {
	be := newFakeBackend()
	r := newRecorder(be, Options{CloseDelay: 100 * time.Millisecond})
	defer r.Close()
	_ = r.Start()
	r.Stop()
	in := be.last(t)
	if in.isClosed() {
		t.Fatal("closed at once")
	}
	// A quick next recording reuses it.
	_ = r.Start()
	r.Stop()
	if len(be.opened()) != 1 {
		t.Errorf("%d opens", len(be.opened()))
	}
	eventually(t, "the device to close", in.isClosed)
	if _, ok := r.Device(); ok {
		t.Error("Device() reports a closed microphone")
	}
	// The next recording opens it again.
	_ = r.Start()
	r.Stop()
	if len(be.opened()) != 2 {
		t.Errorf("%d opens", len(be.opened()))
	}
}

func TestDeviceSelectionAndFallback(t *testing.T) {
	for _, tt := range []struct {
		name, device, wantID, notice string
		setup                        func(*fakeBackend)
	}{
		{name: "default", device: "", wantID: ""},
		{name: "by name", device: "USB Headset", wantID: "bb02"},
		{name: "by name, any case", device: "usb headset", wantID: "bb02"},
		{name: "by ID", device: "bb02", wantID: "bb02"},
		{name: "missing", device: "Yeti", wantID: "", notice: `The microphone "Yeti" is not connected; using the default microphone (Built-in Microphone).`},
		{
			name: "fails to open", device: "USB Headset", wantID: "", notice: `"USB Headset" could not be opened (busy); using the default microphone (Built-in Microphone).`,
			setup: func(b *fakeBackend) { b.openErr["bb02"] = errors.New("busy") },
		},
		{
			name: "listing fails", device: "USB Headset", wantID: "",
			setup: func(b *fakeBackend) { b.listErr = errors.New("no") },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			be := newFakeBackend()
			if tt.setup != nil {
				tt.setup(be)
			}
			var n notices
			r := newRecorder(be, Options{Device: tt.device, Notify: n.add})
			defer r.Close()
			if err := r.Start(); err != nil {
				t.Fatal(err)
			}
			r.Stop()
			if id := be.last(t).id; id != tt.wantID {
				t.Errorf("opened %q, want %q", id, tt.wantID)
			}
			if got := n.all(); tt.notice == "" && got != "" || !strings.Contains(got, tt.notice) {
				t.Errorf("notices %q, want %q", got, tt.notice)
			}
		})
	}
}

func TestOpenFailure(t *testing.T) {
	be := newFakeBackend()
	be.openErr[""] = errors.New("permission denied")
	r := newRecorder(be, Options{})
	defer r.Close()
	if err := r.Start(); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("Start = %v", err)
	}
	if err := r.Start(); err == nil {
		t.Error("second Start succeeded with no device")
	}
}

func TestDisconnectDuringRecording(t *testing.T) {
	for _, tt := range []struct {
		name        string
		defaultRate int
		want        int
		notice      string
	}{
		{"same rate: the recording carries on", 16000, 1500, `The microphone "USB Headset" is not connected; using the default`},
		{"other rate: the recording keeps what it has", 48000, 1000, "The rest of this recording is lost"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			be := newFakeBackend()
			be.rates[""] = tt.defaultRate
			var n notices
			r := newRecorder(be, Options{Device: "USB Headset", Notify: n.add})
			defer r.Close()
			_ = r.Start()
			headset := be.last(t)
			headset.feed(ramp(0, 1000))

			// Unplugged: the device stops and leaves the list.
			be.mu.Lock()
			be.devs = be.devs[:1]
			be.mu.Unlock()
			headset.onStop()
			eventually(t, "a new device", func() bool { return len(be.opened()) == 2 })
			if !headset.isClosed() {
				t.Error("the lost device was not released")
			}
			fallback := be.last(t)
			fallback.feed(ramp(1000, 500))
			got := r.Stop()
			if len(got) != tt.want || !slices.Equal(got, ramp(0, tt.want)) {
				t.Errorf("recorded %d samples, want %d", len(got), tt.want)
			}
			msgs := n.all()
			if !strings.Contains(msgs, `"USB Headset" stopped`) || !strings.Contains(msgs, tt.notice) {
				t.Errorf("notices %q", msgs)
			}
		})
	}
}

func TestStopCallbackOnCloseIsNotALoss(t *testing.T) {
	be := newFakeBackend()
	var n notices
	r := newRecorder(be, Options{CloseDelay: -1, Notify: n.add})
	_ = r.Start()
	in := be.last(t)
	r.Stop()
	in.onStop() // as a device may report its own requested stop
	time.Sleep(20 * time.Millisecond)
	r.Close()
	if msgs := n.all(); msgs != "" || len(be.opened()) != 1 {
		t.Errorf("notices %q, %d opens", msgs, len(be.opened()))
	}
}

func TestSetDevice(t *testing.T) {
	be := newFakeBackend()
	r := newRecorder(be, Options{CloseDelay: time.Hour})
	defer r.Close()
	_ = r.Start()
	builtin := be.last(t)
	r.SetDevice("USB Headset") // during a recording: takes effect after it
	builtin.feed(ramp(0, 100))
	if got := r.Stop(); len(got) != 100 {
		t.Errorf("recording cut short: %d samples", len(got))
	}
	if !builtin.isClosed() {
		t.Error("the old microphone stayed open after the recording")
	}
	_ = r.Start()
	r.Stop()
	if id := be.last(t).id; id != "bb02" {
		t.Errorf("opened %q after SetDevice", id)
	}
	r.SetDevice("") // idle: closes at once
	if !be.last(t).isClosed() {
		t.Error("idle microphone not closed by SetDevice")
	}
}

func TestCloseEndsEverything(t *testing.T) {
	be := newFakeBackend()
	r := newRecorder(be, Options{})
	_ = r.Start()
	frames := r.Frames()
	be.last(t).feed(ramp(0, 10))
	r.Close()
	r.Close()
	for range frames { //nolint:revive // drain until closed
	}
	if !be.last(t).isClosed() {
		t.Error("device left open")
	}
	if err := r.Start(); !errors.Is(err, ErrClosed) {
		t.Errorf("Start after Close: %v", err)
	}
	if err := r.Open(); !errors.Is(err, ErrClosed) {
		t.Errorf("Open after Close: %v", err)
	}
	if r.Stop() != nil {
		t.Error("Stop after Close returned samples")
	}
}

func TestDroppedWhenThePumpFallsBehind(t *testing.T) {
	be := newFakeBackend()
	r := newRecorder(be, Options{})
	defer r.Close()
	_ = r.Start()
	in := be.last(t)
	// Hold the pump off and overfill the 2 s (131072-sample) ring at once.
	r.mu.Lock()
	in.onData(ramp(0, 3*48000))
	r.mu.Unlock()
	got := r.Stop()
	if want := uint64(3*48000 - 131072); r.Dropped() != want || len(got) != 131072 {
		t.Errorf("dropped %d, kept %d; want %d dropped", r.Dropped(), len(got), want)
	}
}

func TestFindDevice(t *testing.T) {
	devs := []Device{{ID: "01", Name: "Mic"}, {ID: "02", Name: "mic"}, {ID: "Mic", Name: "Other"}}
	for want, id := range map[string]string{"Mic": "Mic", "mic": "02", "MIC": "01", "02": "02"} {
		if d, ok := findDevice(devs, want); !ok || d.ID != id {
			t.Errorf("findDevice(%q) = %+v, want ID %s", want, d, id)
		}
	}
	if _, ok := findDevice(devs, "nope"); ok {
		t.Error("found a missing device")
	}
}
