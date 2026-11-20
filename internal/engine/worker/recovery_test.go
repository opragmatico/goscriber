package worker

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/engine"
	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine/autounload"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/output"
	"github.com/opragmatico/goscriber/internal/pipeline"
)

// The acceptance test of process isolation: the worker is SIGKILLed while
// the coordinator streams a dictation into it. The dictation still delivers
// a transcript (the coordinator decodes the recording whole in a new
// worker), the user hears about the crash, and the next dictation streams
// as usual.
func TestDictationSurvivesAWorkerKilledMidStream(t *testing.T) {
	h := newHarness(t, harnessOptions{script: "stream"})
	rec := newLiveRecorder()
	out := &deliveries{}
	c := pipeline.New(pipeline.Options{
		Recorder: rec,
		Engine:   autounload.New(h.Supervisor, autounload.Never, nil),
		Models:   staticModel{},
		Output:   out,
		Notices:  h.notices,
		Settings: func() config.Settings {
			s := config.Defaults()
			s.SelectedModel = "fake"
			return s
		},
	})
	defer c.Close()
	events, stop := c.Subscribe()
	defer stop()
	ev := &eventWatch{ch: events}

	// The first dictation: kill the worker once the overlay shows words.
	c.Start()
	ev.wait(t, "the first interim", func(e pipeline.Event) bool { return e.Kind == pipeline.EventInterim })
	killed := h.kill(t)
	// The recording goes on after the crash, well past the shortest one the
	// speech gate passes (min_recording_ms, 250 ms by default): one it
	// drops ends without a result.
	rec.waitRecorded(t, max(rec.recorded()+150*time.Millisecond, 500*time.Millisecond))
	c.Stop()
	res := ev.wait(t, "the first result", func(e pipeline.Event) bool { return e.Kind == pipeline.EventResult })
	if res.Text != wholeText {
		t.Errorf("first dictation delivered %q, want %q from the whole-recording fallback", res.Text, wholeText)
	}
	ev.wait(t, "idle", func(e pipeline.Event) bool { return e.Kind == pipeline.EventState && e.State == pipeline.StateIdle })
	wantCodes(t, h.notices, notice.CodeEngineCrashed)

	// The next dictation streams in the new worker.
	c.Start()
	ev.wait(t, "the second interim", func(e pipeline.Event) bool { return e.Kind == pipeline.EventInterim })
	rec.waitRecorded(t, 600*time.Millisecond) // every word of the dictation, at 100 ms a word
	c.Stop()
	res = ev.wait(t, "the second result", func(e pipeline.Event) bool { return e.Kind == pipeline.EventResult })
	if res.Text != streamText {
		t.Errorf("second dictation delivered %q, want the stream's %q", res.Text, streamText)
	}
	if w := h.worker(); w == nil || w == killed {
		t.Error("the second dictation did not run in a new worker")
	}
	if got := out.texts(); !slices.Equal(got, []string{wholeText, streamText}) {
		t.Errorf("delivered %q", got)
	}
	wantCodes(t, h.notices, notice.CodeEngineCrashed)
}

// staticModel always selects the fake model, on the CPU, so a crash is a
// crash and not a GPU fault.
type staticModel struct{}

func (staticModel) Resolve(config.Settings) (pipeline.Model, error) {
	return pipeline.Model{Spec: engine.LoadSpec{ID: "fake", Path: "fake.gguf", CPU: true}, Name: "Fake"}, nil
}

// liveRecorder records a loud tone in real time: 10 ms frames every 10 ms
// from Start to Stop. A busy machine misses ticks, so the audio can fall
// short of the time spent recording: wait for it with waitRecorded, not
// with a sleep.
type liveRecorder struct {
	levels chan audio.Level

	mu      sync.Mutex
	frames  chan []float32
	samples []float32
	stop    chan struct{}
	done    chan struct{}
}

func newLiveRecorder() *liveRecorder { return &liveRecorder{levels: make(chan audio.Level)} }

func (r *liveRecorder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = make(chan []float32, 1000)
	r.samples = nil
	r.stop, r.done = make(chan struct{}), make(chan struct{})
	frames, stop, done := r.frames, r.stop, r.done
	go func() {
		defer close(done)
		t := time.NewTicker(10 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				close(frames)
				return
			case <-t.C:
				f := speech(160)
				r.mu.Lock()
				r.samples = append(r.samples, f...)
				r.mu.Unlock()
				frames <- f
			}
		}
	}()
	return nil
}

func (r *liveRecorder) Stop() []float32 {
	r.mu.Lock()
	stop, done := r.stop, r.done
	r.mu.Unlock()
	close(stop)
	<-done
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.samples)
}

func (r *liveRecorder) SampleRate() int { return 16000 }

// recorded is how much audio the current recording holds.
func (r *liveRecorder) recorded() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return time.Duration(len(r.samples)) * time.Second / time.Duration(r.SampleRate())
}

// waitRecorded waits until the current recording holds at least d of
// audio.
func (r *liveRecorder) waitRecorded(t *testing.T, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for r.recorded() < d {
		if time.Now().After(deadline) {
			t.Fatalf("the recording holds %v of audio, want %v", r.recorded(), d)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *liveRecorder) Frames() <-chan []float32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.frames
}

func (r *liveRecorder) Levels() <-chan audio.Level { return r.levels }

// deliveries records what was pasted.
type deliveries struct {
	mu  sync.Mutex
	got []string
}

func (d *deliveries) Deliver(_ context.Context, text string, _ output.Options) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.got = append(d.got, text)
	return nil
}

func (d *deliveries) texts() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.got)
}

// eventWatch reads the coordinator's events in order.
type eventWatch struct{ ch <-chan pipeline.Event }

func (w *eventWatch) wait(t *testing.T, what string, match func(pipeline.Event) bool) pipeline.Event {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-w.ch:
			if !ok {
				t.Fatalf("events ended before %s", what)
			}
			if match(e) {
				return e
			}
		case <-timeout:
			t.Fatalf("no %s", what)
		}
	}
}
