package audio

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

//go:generate go run gen_sounds.go

var (
	//go:embed sounds/start.wav
	startWAV []byte
	//go:embed sounds/stop.wav
	stopWAV []byte
)

// Sound is a mono clip.
type Sound struct {
	Samples []float32
	Rate    int
}

// Duration is how long the sound plays.
func (s Sound) Duration() time.Duration {
	if s.Rate <= 0 {
		return 0
	}
	return time.Duration(len(s.Samples)) * time.Second / time.Duration(s.Rate)
}

var (
	cuesOnce          sync.Once
	startCue, stopCue Sound
)

// StartCue is the sound that says recording is starting: a short rising
// chime. StopCue is its falling counterpart. Both were made for Goscriber
// (gen_sounds.go) and are embedded in the binary.
func StartCue() Sound { loadCues(); return startCue }

// StopCue is the sound that says recording has stopped.
func StopCue() Sound { loadCues(); return stopCue }

func loadCues() {
	cuesOnce.Do(func() {
		startCue = mustDecode(startWAV)
		stopCue = mustDecode(stopWAV)
	})
}

func mustDecode(b []byte) Sound {
	samples, rate, err := ReadWAV(bytes.NewReader(b))
	if err != nil {
		panic(fmt.Sprintf("audio: embedded cue: %v", err))
	}
	return Sound{Samples: samples, Rate: rate}
}

// playoutTail is how long a speaker keeps playing after its last samples
// were handed over: the device's buffers, a few 10 ms periods.
const playoutTail = 40 * time.Millisecond

// Player plays sounds on an output device.
type Player struct{ be backend }

// NewPlayer returns a Player on the system's speakers and headphones.
func NewPlayer() *Player { return &Player{be: systemBackend()} }

// Play plays s on the output device named device (its name or ID; "" is the
// system default, which a device that is not connected falls back to) at
// volume, from 0 to 1, and returns once it has been heard. Cancelling ctx
// stops it.
func (p *Player) Play(ctx context.Context, s Sound, device string, volume float64) error {
	if len(s.Samples) == 0 || volume <= 0 {
		return nil
	}
	buf := make([]float32, len(s.Samples))
	gain := float32(min(volume, 1))
	for i, v := range s.Samples {
		buf[i] = v * gain
	}

	pos := 0 // the audio thread's alone
	var finished atomic.Bool
	done := make(chan struct{})
	fill := func(out []float32) {
		n := copy(out, buf[pos:])
		pos += n
		clear(out[n:])
		if pos == len(buf) && finished.CompareAndSwap(false, true) {
			close(done)
		}
	}

	id := p.resolve(device)
	st, err := p.be.openOutput(id, s.Rate, fill)
	if err != nil && id != "" {
		st, err = p.be.openOutput("", s.Rate, fill)
	}
	if err != nil {
		return fmt.Errorf("audio: open speaker: %w", err)
	}
	defer st.close()
	if err := st.start(); err != nil {
		return fmt.Errorf("audio: start speaker: %w", err)
	}
	timeout := time.NewTimer(s.Duration() + time.Second)
	defer timeout.Stop()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	case <-timeout.C:
		return errors.New("audio: the speaker stopped taking samples")
	}
	tail := time.NewTimer(playoutTail)
	defer tail.Stop()
	select {
	case <-tail.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// resolve returns the ID of the output device to open, or "" for the
// default.
func (p *Player) resolve(device string) string {
	if device == "" {
		return ""
	}
	devs, err := p.be.devices(playbackDevices)
	if err != nil {
		return ""
	}
	if d, ok := findDevice(devs, device); ok && !d.Default {
		return d.ID
	}
	return ""
}

// FeedbackOptions say what Feedback does around a recording.
type FeedbackOptions struct {
	// Sounds plays the start and stop cues.
	Sounds bool
	// Volume is the cues' volume, from 0 to 1.
	Volume float64
	// Device is the output device for the cues, by name or ID; "" is the
	// system default.
	Device string
	// Mute mutes the system's default output while recording, so music or
	// a call does not end up in the transcript.
	Mute bool
}

// Muter mutes the system's default output and returns a function that puts
// back how it was.
type Muter func() (restore func() error, err error)

// player is what Feedback plays cues with; *Player implements it.
type player interface {
	Play(ctx context.Context, s Sound, device string, volume float64) error
}

// cueTimeout bounds how long a cue may hold up the start of a recording.
const cueTimeout = 2 * time.Second

// Feedback plays the start and stop cues and mutes other audio while
// recording. It is safe for concurrent use.
type Feedback struct {
	player player
	mute   Muter
	log    *slog.Logger

	mu       sync.Mutex
	restore  func() error // unmutes, while muted
	warned   map[string]bool
	stopping sync.WaitGroup // stop cues still playing
}

// NewFeedback returns a Feedback that plays cues with p and mutes the output
// with mute, which may be nil where muting is not supported. A nil log
// means slog's default logger.
func NewFeedback(p *Player, mute Muter, log *slog.Logger) *Feedback {
	return newFeedback(p, mute, log)
}

func newFeedback(p player, mute Muter, log *slog.Logger) *Feedback {
	if log == nil {
		log = slog.Default()
	}
	return &Feedback{player: p, mute: mute, log: log, warned: map[string]bool{}}
}

// Start runs just before a recording starts. It plays the start cue to its
// end, so the microphone does not record it, then mutes other audio. It
// reports whether the cue played: an always-on microphone's pre-roll then
// holds the cue and should be dropped.
func (f *Feedback) Start(o FeedbackOptions) (played bool) {
	if o.Sounds && o.Volume > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), cueTimeout)
		err := f.player.Play(ctx, StartCue(), o.Device, o.Volume)
		cancel()
		if err != nil {
			f.warn("start sound", "the start sound could not be played", err)
		}
		played = err == nil
	}
	if o.Mute {
		f.muteOutput()
	}
	return played
}

// Stop runs once a recording has ended, cancelled or not. It puts other
// audio back as it was and, unless the recording was cancelled, plays the
// stop cue without waiting for it.
func (f *Feedback) Stop(o FeedbackOptions, cancelled bool) {
	f.unmute()
	if cancelled || !o.Sounds || o.Volume <= 0 {
		return
	}
	f.stopping.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cueTimeout)
		defer cancel()
		if err := f.player.Play(ctx, StopCue(), o.Device, o.Volume); err != nil {
			f.warn("stop sound", "the stop sound could not be played", err)
		}
	})
}

// Close puts other audio back as it was, if a recording left it muted, and
// waits for a stop cue still playing.
func (f *Feedback) Close() {
	f.unmute()
	f.stopping.Wait()
}

// Muted reports whether Feedback muted the output and has not restored it.
func (f *Feedback) Muted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.restore != nil
}

func (f *Feedback) muteOutput() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.restore != nil || f.mute == nil {
		if f.mute == nil {
			f.warnLocked("mute", "muting other audio while recording is not supported on this system", nil)
		}
		return
	}
	restore, err := f.mute()
	if err != nil {
		f.warnLocked("mute", "other audio could not be muted while recording", err)
		return
	}
	f.restore = restore
}

func (f *Feedback) unmute() {
	f.mu.Lock()
	restore := f.restore
	f.restore = nil
	f.mu.Unlock()
	if restore == nil {
		return
	}
	if err := restore(); err != nil {
		f.log.Warn("the output could not be unmuted after recording", "err", err)
	}
}

// warn logs a problem as a warning the first time and at debug level after
// that, so a missing speaker does not fill the log one recording at a time.
func (f *Feedback) warn(kind, msg string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.warnLocked(kind, msg, err)
}

func (f *Feedback) warnLocked(kind, msg string, err error) {
	args := []any{}
	if err != nil {
		args = append(args, "err", err)
	}
	if f.warned[kind] {
		f.log.Debug(msg, args...)
		return
	}
	f.warned[kind] = true
	f.log.Warn(msg, args...)
}
