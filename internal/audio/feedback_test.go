package audio

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeOutputStream plays into a buffer as fast as fill fills it.
type fakeOutputStream struct {
	id   string
	hz   int
	fill func([]float32)

	mu      sync.Mutex
	played  []float32
	started bool
	stop    chan struct{}
	done    chan struct{}
}

func (b *fakeBackend) openOutput(id string, rate int, fill func([]float32)) (outputStream, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.openErr["out:"+id]; err != nil {
		return nil, err
	}
	o := &fakeOutputStream{id: id, hz: rate, fill: fill, stop: make(chan struct{}), done: make(chan struct{})}
	b.outputs = append(b.outputs, o)
	return o, nil
}

func (o *fakeOutputStream) start() error {
	o.mu.Lock()
	o.started = true
	o.mu.Unlock()
	go func() {
		defer close(o.done)
		buf := make([]float32, max(1, o.hz/100))
		for {
			select {
			case <-o.stop:
				return
			case <-time.After(time.Millisecond):
			}
			o.fill(buf)
			o.mu.Lock()
			o.played = append(o.played, buf...)
			o.mu.Unlock()
		}
	}()
	return nil
}

func (o *fakeOutputStream) close() {
	o.mu.Lock()
	started := o.started
	o.mu.Unlock()
	close(o.stop)
	if started {
		<-o.done
	}
}

func (o *fakeOutputStream) samples() []float32 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.played)
}

func TestCues(t *testing.T) {
	for name, s := range map[string]Sound{"start": StartCue(), "stop": StopCue()} {
		d := s.Duration()
		if s.Rate != 24000 || d < 150*time.Millisecond || d > 300*time.Millisecond {
			t.Errorf("%s cue: %d Hz, %v", name, s.Rate, d)
		}
		if l := Measure(s.Samples); l.Peak < 0.45 || l.Peak > 0.55 {
			t.Errorf("%s cue peaks at %v, want about -6 dBFS", name, l.Peak)
		}
		if last := s.Samples[len(s.Samples)-1]; math.Abs(float64(last)) > 0.001 {
			t.Errorf("%s cue ends at %v: it would click", name, last)
		}
	}
	if slices.Equal(StartCue().Samples, StopCue().Samples) {
		t.Error("the start and stop cues should differ")
	}
}

func TestPlayerPlaysOnTheChosenDeviceAtVolume(t *testing.T) {
	be := newFakeBackend()
	p := &Player{be: be}
	s := Sound{Samples: []float32{0.5, -0.5, 0.25}, Rate: 16000}

	if err := p.Play(context.Background(), s, "USB Headset", 0.5); err != nil {
		t.Fatal(err)
	}
	o := be.outputs[0]
	if o.id != "bb02" || o.hz != 16000 {
		t.Errorf("played on %q at %d Hz, want the headset at the sound's rate", o.id, o.hz)
	}
	if got := o.samples(); len(got) < 3 || !slices.Equal(got[:4], []float32{0.25, -0.25, 0.125, 0}) {
		t.Errorf("played %v, want the sound at half volume, then silence", got[:min(4, len(got))])
	}

	// The default device by name, a device that is not connected, and one
	// that fails to open all play on the system default.
	be.openErr["out:bb02"] = errors.New("busy")
	for _, device := range []string{"Built-in Microphone", "Gone", "USB Headset", ""} {
		before := len(be.outputs)
		if err := p.Play(context.Background(), s, device, 1); err != nil {
			t.Fatalf("%q: %v", device, err)
		}
		if last := be.outputs[len(be.outputs)-1]; last.id != "" || len(be.outputs) == before {
			t.Errorf("%q played on %q, want the default", device, last.id)
		}
	}

	// Silence plays nothing.
	n := len(be.outputs)
	if err := p.Play(context.Background(), s, "", 0); err != nil || len(be.outputs) != n {
		t.Errorf("a volume of 0 opened a device (err %v)", err)
	}
	be.openErr["out:"] = errors.New("no speaker")
	if err := p.Play(context.Background(), s, "", 1); err == nil {
		t.Error("want an error without a speaker")
	}
}

func TestPlayerCancel(t *testing.T) {
	be := newFakeBackend()
	p := &Player{be: be}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	long := Sound{Samples: make([]float32, 16000*10), Rate: 16000}
	if err := p.Play(ctx, long, "", 1); !errors.Is(err, context.Canceled) {
		t.Errorf("err %v, want cancelled", err)
	}
}

// fakePlayer and fakeMute record what Feedback does, in order.
type feedbackLog struct {
	mu     sync.Mutex
	events []string
	played chan string
	err    error
}

func (l *feedbackLog) add(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *feedbackLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

func (l *feedbackLog) Play(_ context.Context, s Sound, device string, _ float64) error {
	name := "start"
	if len(s.Samples) > 0 && &s.Samples[0] == &StopCue().Samples[0] {
		name = "stop"
	}
	l.add("play " + name + " on " + device)
	if l.played != nil {
		l.played <- name
	}
	return l.err
}

func (l *feedbackLog) mute() (func() error, error) {
	l.add("mute")
	return func() error { l.add("unmute"); return nil }, nil
}

func TestFeedbackAroundARecording(t *testing.T) {
	l := &feedbackLog{played: make(chan string, 4)}
	f := newFeedback(l, l.mute, slog.New(slog.DiscardHandler))
	o := FeedbackOptions{Sounds: true, Volume: 0.8, Device: "Speakers", Mute: true}

	if !f.Start(o) {
		t.Error("Start should report that the cue played")
	}
	<-l.played
	if got := l.get(); !slices.Equal(got, []string{"play start on Speakers", "mute"}) {
		t.Errorf("start: %v, want the cue then the mute", got)
	}
	if !f.Muted() {
		t.Error("the output should be muted while recording")
	}
	f.Stop(o, false)
	<-l.played
	f.Close()
	if got := l.get(); !slices.Equal(got[2:], []string{"unmute", "play stop on Speakers"}) {
		t.Errorf("stop: %v, want the unmute then the cue", got)
	}

	// A cancel unmutes and plays nothing.
	f.Start(o)
	<-l.played
	f.Stop(o, true)
	f.Close()
	if got := l.get(); !slices.Equal(got[4:], []string{"play start on Speakers", "mute", "unmute"}) {
		t.Errorf("cancel: %v", got)
	}
	if f.Muted() {
		t.Error("the output should be unmuted after a cancel")
	}
}

func TestFeedbackOff(t *testing.T) {
	l := &feedbackLog{}
	f := newFeedback(l, l.mute, slog.New(slog.DiscardHandler))
	if f.Start(FeedbackOptions{Volume: 1}) {
		t.Error("no cue should play with sounds off")
	}
	f.Stop(FeedbackOptions{Volume: 1}, false)
	f.Start(FeedbackOptions{Sounds: true, Volume: 0})
	f.Close()
	if got := l.get(); len(got) != 0 {
		t.Errorf("did %v with sounds and mute off", got)
	}
}

func TestFeedbackSurvivesFailures(t *testing.T) {
	l := &feedbackLog{err: errors.New("no speaker")}
	f := newFeedback(l, func() (func() error, error) { return nil, errors.New("no mute control") }, slog.New(slog.DiscardHandler))
	o := FeedbackOptions{Sounds: true, Volume: 1, Mute: true}
	if f.Start(o) {
		t.Error("a cue that failed did not play")
	}
	if f.Muted() {
		t.Error("a failed mute leaves nothing to restore")
	}
	f.Stop(o, false)
	f.Close()

	// Without a muter (not macOS), muting is skipped.
	f = newFeedback(&feedbackLog{}, nil, slog.New(slog.DiscardHandler))
	f.Start(FeedbackOptions{Mute: true})
	if f.Muted() {
		t.Error("nothing to mute with")
	}
	f.Close()
}

func TestDropPreRoll(t *testing.T) {
	be := newFakeBackend()
	r := newRecorder(be, Options{AlwaysOn: true, PreRoll: 100 * time.Millisecond})
	defer r.Close()
	if err := r.Open(); err != nil {
		t.Fatal(err)
	}
	in := be.last(t)
	cue := make([]float32, 48000/10)
	for i := range cue {
		cue[i] = 0.5
	}
	in.feed(cue) // the start cue, heard by the microphone
	r.DropPreRoll()
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	in.feed(make([]float32, 480))
	got := r.Stop()
	if l := Measure(got); l.Peak != 0 || len(got) != 480 {
		t.Errorf("%d samples peaking at %v: the cue in the pre-roll should be gone", len(got), l.Peak)
	}
}
