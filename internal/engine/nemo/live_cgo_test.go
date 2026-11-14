//go:build cgo && nemo

package nemo

import (
	"context"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/output"
	"github.com/opragmatico/goscriber/internal/pipeline"
)

// These tests run the dictation coordinator on the real engine, with a
// recording played into it in 10 ms frames as a microphone would deliver
// them, and measure what the user sees: when words appear while they speak,
// and how long the transcript takes once they stop.

// Release budgets on Apple Silicon: from the end of the recording to the
// transcript being handed to the output, which then pastes it after
// paste_delay_ms (60 ms by default).
const (
	maxStreamRelease  = 300 * time.Millisecond
	maxChunkedRelease = 500 * time.Millisecond
	// maxInterimLag is how long after a word ends it may take to show.
	maxInterimLag = 500 * time.Millisecond
)

// playback is a pipeline.Recorder that plays samples, speed times faster
// than real time.
type playback struct {
	samples []float32
	rate    int
	speed   float64
	levels  chan audio.Level

	mu      sync.Mutex
	frames  chan []float32
	played  chan struct{} // closed once the last frame is out
	started time.Time
}

func (p *playback) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.frames = make(chan []float32, 64)
	p.played = make(chan struct{})
	p.started = time.Now()
	go p.play(p.frames, p.played, p.started)
	return nil
}

func (p *playback) play(frames chan<- []float32, played chan struct{}, started time.Time) {
	defer close(played)
	n := p.rate / 100
	for off := 0; off < len(p.samples); off += n {
		end := min(off+n, len(p.samples))
		due := started.Add(time.Duration(float64(time.Duration(end)*time.Second/time.Duration(p.rate)) / p.speed))
		time.Sleep(time.Until(due))
		frames <- slices.Clone(p.samples[off:end])
	}
}

// Stop waits for the playback to end, as a user stops after speaking.
func (p *playback) Stop() []float32 {
	p.mu.Lock()
	frames, played := p.frames, p.played
	p.mu.Unlock()
	<-played
	close(frames)
	return slices.Clone(p.samples)
}

func (p *playback) SampleRate() int            { return p.rate }
func (p *playback) Levels() <-chan audio.Level { return p.levels }

func (p *playback) Frames() <-chan []float32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.frames
}

// at is the wall-clock time the playback reaches d into the samples.
func (p *playback) at(d time.Duration) time.Time {
	return p.started.Add(time.Duration(float64(d) / p.speed))
}

type oneModel struct{ m pipeline.Model }

func (o oneModel) Resolve(config.Settings) (pipeline.Model, error) { return o.m, nil }

type timedOutput struct {
	mu   sync.Mutex
	text string
	at   time.Time
}

func (o *timedOutput) Deliver(_ context.Context, text string, _ output.Options) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.text, o.at = text, time.Now()
	return nil
}

type noNotices struct{ t *testing.T }

func (n noNotices) Publish(x notice.Notice) bool {
	n.t.Errorf("notice %s: %s", x.Code, x.Message)
	return true
}
func (noNotices) Resolve(notice.Code) bool { return true }

// seen is an interim and when it arrived.
type seen struct {
	text string
	at   time.Time
}

// dictation is one session's outcome.
type dictation struct {
	text      string
	languages []string      // the languages the result reports
	release   time.Duration // from the end of the recording to the transcript
	interims  []seen
	play      *playback
}

// dictate records samples through a coordinator on e, asking for English,
// played speed times faster than real time, and waits for the transcript.
func dictate(t *testing.T, e engine.Engine, spec engine.LoadSpec, samples []float32, rate int, speed float64) dictation {
	t.Helper()
	return dictateIn(t, e, spec, "en-US", samples, rate, speed)
}

// dictateIn is dictate in another language.
func dictateIn(t *testing.T, e engine.Engine, spec engine.LoadSpec, lang string, samples []float32, rate int, speed float64) dictation {
	t.Helper()
	play := &playback{samples: samples, rate: rate, speed: speed, levels: make(chan audio.Level)}
	out := &timedOutput{}
	settings := config.Defaults()
	c := pipeline.New(pipeline.Options{
		Recorder: play,
		Engine:   e,
		Models:   oneModel{pipeline.Model{Spec: spec, Name: "test", Language: lang}},
		Output:   out,
		Notices:  noNotices{t},
		Settings: func() config.Settings { return settings },
		Log:      slog.New(slog.DiscardHandler),
	})
	defer c.Close()
	events, stop := c.Subscribe()
	defer stop()

	var d dictation
	d.play = play
	c.Start()
	var stopped time.Time
	for ev := range events {
		switch {
		case ev.Kind == pipeline.EventState && ev.State == pipeline.StateRecording:
			go func() {
				// Stop as soon as the playback ends.
				play.mu.Lock()
				played := play.played
				play.mu.Unlock()
				<-played
				stopped = time.Now()
				c.Stop()
			}()
		case ev.Kind == pipeline.EventInterim:
			d.interims = append(d.interims, seen{ev.Text, time.Now()})
		case ev.Kind == pipeline.EventResult:
			d.languages = ev.Languages
		case ev.Kind == pipeline.EventState && ev.State == pipeline.StateIdle:
			out.mu.Lock()
			d.text = out.text
			d.release = out.at.Sub(stopped)
			out.mu.Unlock()
			return d
		}
	}
	t.Fatal("the coordinator closed its events")
	return d
}

// timingsMeaningful reports whether latencies are worth holding to a budget:
// on Apple Silicon without the race detector.
func timingsMeaningful() bool {
	return !raceEnabled && runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
}

func TestLiveNemotronDictation(t *testing.T) {
	path := testModel(t, nemotronFile)
	samples, rate, want := fixture(t)
	e := New()
	defer func() { _ = e.Unload() }()
	spec := engine.LoadSpec{ID: "nemotron", Path: path}
	if _, err := e.Load(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	// Where each word really is, to measure how soon it shows.
	ref, err := e.Recognize(context.Background(), samples, rate, engine.Options{Language: "en-US", WordTimestamps: true})
	if err != nil {
		t.Fatal(err)
	}
	// A first session builds the stream's compute buffers, as the first
	// dictation after loading does.
	dictate(t, e, spec, samples[:rate], rate, 8)

	d := dictate(t, e, spec, samples, rate, 1)
	if w := wer(want, d.text); w > 0.1 {
		t.Errorf("WER %.1f%%: %q", 100*w, d.text)
	}
	if len(d.interims) < 10 {
		t.Errorf("%d interims over 11 s of speech", len(d.interims))
	}
	var lags []time.Duration
	for i, w := range ref.Words {
		for _, in := range d.interims {
			if len(strings.Fields(in.text)) > i {
				lags = append(lags, in.at.Sub(d.play.at(w.End)))
				break
			}
		}
	}
	if len(lags) < len(ref.Words)*9/10 {
		t.Fatalf("only %d of %d words showed before the end", len(lags), len(ref.Words))
	}
	slices.Sort(lags)
	p90 := lags[len(lags)*9/10]
	t.Logf("%d interims; word shown after it ends: median %v, p90 %v, max %v; release → transcript %v",
		len(d.interims), lags[len(lags)/2].Round(time.Millisecond), p90.Round(time.Millisecond),
		lags[len(lags)-1].Round(time.Millisecond), d.release.Round(time.Millisecond))
	if timingsMeaningful() {
		if p90 > maxInterimLag {
			t.Errorf("90%% of words showed within %v of being spoken, want %v", p90, maxInterimLag)
		}
		if d.release > maxStreamRelease {
			t.Errorf("release → transcript %v, want under %v", d.release, maxStreamRelease)
		}
	}
}

func TestLiveParakeetChunkedDictation(t *testing.T) {
	path := testModel(t, "parakeet-tdt-0.6b-v2.q8_0.gguf")
	clip, rate, ref := fixture(t)
	// A two-minute dictation: the fixture eleven times over, with its own
	// pauses and a short one between takes.
	var samples []float32
	var want []string
	gap := make([]float32, rate*4/10)
	for range 11 {
		samples = append(samples, clip...)
		samples = append(samples, gap...)
		want = append(want, ref)
	}
	e := New()
	defer func() { _ = e.Unload() }()
	spec := engine.LoadSpec{ID: "parakeet", Path: path}
	if _, err := e.Load(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	whole, err := e.Recognize(context.Background(), samples, rate, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	wholeTook := time.Since(start)

	// Eight times real time: still far slower than the model decodes, so
	// at the end only the open chunk is left, as in a real dictation.
	d := dictate(t, e, spec, samples, rate, 8)
	got, ref2 := normalizeWords(d.text), normalizeWords(strings.Join(want, " "))
	t.Logf("2 min 6 s in %d interims; release → transcript %v (decoding it whole takes %v); WER %.2f%% (whole %.2f%%)",
		len(d.interims), d.release.Round(time.Millisecond), wholeTook.Round(time.Millisecond),
		100*wer(strings.Join(want, " "), d.text), 100*wer(strings.Join(want, " "), whole.Text))
	if !slices.Equal(got, ref2) {
		t.Errorf("chunked transcript has %d words, want the %d spoken, none lost or repeated:\n%s", len(got), len(ref2), d.text)
	}
	if len(d.interims) < 4 {
		t.Errorf("%d interims; want chunks decoded while recording", len(d.interims))
	}
	if timingsMeaningful() && d.release > maxChunkedRelease {
		t.Errorf("release → transcript %v, want under %v", d.release, maxChunkedRelease)
	}
}
