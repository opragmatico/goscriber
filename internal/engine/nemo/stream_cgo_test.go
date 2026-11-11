//go:build cgo && nemo

package nemo

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/engine"
)

// maxFinishLatency is the most Finish → end-of-stream final may take on
// Apple Silicon.
const maxFinishLatency = 300 * time.Millisecond

// streamRun is what streaming a clip produced.
type streamRun struct {
	interims  []engine.Result
	finals    []engine.Result // before Finish, from ForceEndpoint
	final     engine.Result   // the end-of-stream final
	finish    time.Duration   // from calling Finish to Next returning the final
	firstText time.Duration   // audio pushed when the first text arrived
}

// streamClip pushes samples in chunks of chunk, draining results after each
// push as a live caller would, then finishes the stream, drains it to
// io.EOF and closes it. endpointAt, when positive, forces an endpoint once
// that much audio is in.
func streamClip(t *testing.T, e *Engine, samples []float32, rate int, chunk, endpointAt time.Duration, opts engine.Options) streamRun {
	t.Helper()
	s, err := e.StartStream(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var run streamRun
	drain := func(pushed time.Duration) {
		for {
			r, err := s.Next()
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if r == nil {
				return
			}
			if r.Final {
				run.finals = append(run.finals, *r)
			} else {
				run.interims = append(run.interims, *r)
			}
			if r.Text != "" && run.firstText == 0 {
				run.firstText = pushed
			}
		}
	}
	n := int(chunk * time.Duration(rate) / time.Second)
	endpointed := false
	for off := 0; off < len(samples); off += n {
		if err := s.Push(samples[off:min(off+n, len(samples))], rate); err != nil {
			t.Fatalf("Push: %v", err)
		}
		pushed := time.Duration(min(off+n, len(samples))) * time.Second / time.Duration(rate)
		if endpointAt > 0 && !endpointed && pushed >= endpointAt {
			if err := s.ForceEndpoint(); err != nil {
				t.Fatalf("ForceEndpoint: %v", err)
			}
			endpointed = true
		}
		drain(pushed)
	}

	start := time.Now()
	if err := s.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	r, err := s.Next()
	run.finish = time.Since(start)
	if err != nil || r == nil {
		t.Fatalf("Next after Finish = %v, %v; want the end-of-stream final", r, err)
	}
	run.final = *r
	if r, err := s.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next after the final = %v, %v; want io.EOF", r, err)
	}
	if _, err := s.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next after io.EOF = %v; want io.EOF again", err)
	}
	return run
}

func TestNemotronStreamsFixture(t *testing.T) {
	samples, rate, want := fixture(t)
	clip := time.Duration(len(samples)) * time.Second / time.Duration(rate)
	e, info := loadNemotron(t, false)
	if !info.Streaming {
		t.Fatal("Nemotron 3.5 does not report streaming")
	}
	offline, err := e.Recognize(context.Background(), samples, rate, engine.Options{Language: "en-US"})
	if err != nil {
		t.Fatal(err)
	}

	run := streamClip(t, e, samples, rate, 20*time.Millisecond, 0,
		engine.Options{Language: "en-US", InterimResults: true, WordTimestamps: true})
	t.Logf("%d interims, the first at %v of audio; Finish → final %v: %q",
		len(run.interims), run.firstText, run.finish.Round(100*time.Microsecond), run.final.Text)

	if len(run.interims) < 5 {
		t.Errorf("%d interim results, want them to keep arriving while audio does", len(run.interims))
	}
	if len(run.finals) != 0 {
		t.Errorf("finals before Finish with endpointing off: %v", run.finals)
	}
	last := run.interims[len(run.interims)-1]
	if !strings.HasPrefix(strings.ToLower(last.Text), "and so my fellow americans") {
		t.Errorf("last interim %q", last.Text)
	}
	if w := wer(offline.Text, run.final.Text); w > maxWER {
		t.Errorf("streamed final differs from the offline result: WER %.2f\nstream  %q\noffline %q", w, run.final.Text, offline.Text)
	}
	if w := wer(want, run.final.Text); w > maxWER {
		t.Errorf("WER %.2f: %q", w, run.final.Text)
	}
	if !run.final.Final || run.final.AudioProcessed < clip-100*time.Millisecond {
		t.Errorf("end-of-stream final: Final %v over %v, want all %v", run.final.Final, run.final.AudioProcessed, clip)
	}
	if len(run.final.Words) == 0 || !slices.Contains(run.final.Languages, "en-US") {
		t.Errorf("final words %v, languages %v", run.final.Words, run.final.Languages)
	}
	checkFinishLatency(t, run.finish)
}

// checkFinishLatency holds Finish → final under maxFinishLatency on Apple
// Silicon without the race detector, where the number means something.
func checkFinishLatency(t *testing.T, d time.Duration) {
	t.Helper()
	if raceEnabled || runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return
	}
	if d > maxFinishLatency {
		t.Errorf("Finish → final took %v, want under %v", d, maxFinishLatency)
	}
}

// The latency presets, on Metal and on the CPU: the numbers the PR quotes.
func TestNemotronStreamLatencyPresets(t *testing.T) {
	path := testModel(t, nemotronFile)
	samples, rate, want := fixture(t)
	for _, cpu := range []bool{false, true} {
		for _, l := range []engine.Latency{engine.LatencyLow, engine.LatencyBalanced, engine.LatencyAccurate} {
			e := New()
			if _, err := e.Load(context.Background(), engine.LoadSpec{Path: path, CPU: cpu, Latency: l}); err != nil {
				t.Fatal(err)
			}
			// The best of three runs: the first pays for warming up.
			var best streamRun
			for i := range 3 {
				run := streamClip(t, e, samples, rate, 20*time.Millisecond, 0, engine.Options{Language: "en-US", InterimResults: true})
				if i == 0 || run.finish < best.finish {
					best = run
				}
			}
			if err := e.Unload(); err != nil {
				t.Fatal(err)
			}
			backend := "metal"
			if cpu {
				backend = "cpu"
			}
			t.Logf("%-5s %-8s Finish → final %5.1f ms, %3d interims, first text after %v, WER %.2f",
				backend, l, float64(best.finish.Microseconds())/1000, len(best.interims), best.firstText, wer(want, best.final.Text))
			if w := wer(want, best.final.Text); w > maxWER {
				t.Errorf("%s %s: WER %.2f: %q", backend, l, w, best.final.Text)
			}
			if !cpu {
				checkFinishLatency(t, best.finish)
			}
		}
	}
}

func TestNemotronStreamResamples(t *testing.T) {
	samples, rate, want := fixture(t)
	e, _ := loadNemotron(t, false)
	up := make([]float32, 0, 3*len(samples))
	for _, s := range samples {
		up = append(up, s, s, s)
	}
	run := streamClip(t, e, up, 3*rate, 10*time.Millisecond, 0, engine.Options{InterimResults: true})
	if w := wer(want, run.final.Text); w > maxWER {
		t.Errorf("48 kHz in 10 ms chunks: WER %.2f: %q", w, run.final.Text)
	}
}

func TestNemotronStreamForceEndpoint(t *testing.T) {
	samples, rate, want := fixture(t)
	e, _ := loadNemotron(t, false)
	run := streamClip(t, e, samples, rate, 20*time.Millisecond, 6*time.Second, engine.Options{InterimResults: true})
	if len(run.finals) != 1 {
		t.Fatalf("%d finals after ForceEndpoint, want 1: %v", len(run.finals), run.finals)
	}
	first := run.finals[0]
	t.Logf("endpoint final %q at %v; end-of-stream final %q", first.Text, first.AudioProcessed, run.final.Text)
	if first.Text == "" || first.AudioProcessed > 6500*time.Millisecond {
		t.Errorf("endpoint final %+v, want the speech of the first 6 s", first)
	}
	if w := wer(want, first.Text+" "+run.final.Text); w > maxWER {
		t.Errorf("the two utterances together: WER %.2f: %q + %q", w, first.Text, run.final.Text)
	}
}

// Without InterimResults a stream yields only finals, though the library
// produces interims regardless.
func TestNemotronStreamWithoutInterims(t *testing.T) {
	samples, rate, want := fixture(t)
	e, _ := loadNemotron(t, false)
	run := streamClip(t, e, samples, rate, 20*time.Millisecond, 0, engine.Options{})
	if len(run.interims) != 0 {
		t.Errorf("%d interims without InterimResults, the first %q", len(run.interims), run.interims[0].Text)
	}
	if w := wer(want, run.final.Text); w > maxWER {
		t.Errorf("WER %.2f: %q", w, run.final.Text)
	}
}

func TestNemotronStreamMisuse(t *testing.T) {
	samples, rate, _ := fixture(t)
	e, _ := loadNemotron(t, false)
	s, err := e.StartStream(context.Background(), engine.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Push(nil, 0); err != nil {
		t.Errorf("empty Push = %v", err)
	}
	wantKind(t, s.Push(samples, 4000), engine.ErrInvalidArgument)
	if err := s.Push(samples[:1600], rate); err != nil {
		t.Fatal(err)
	}
	wantKind(t, s.Push(samples[:1600], 48000), engine.ErrInvalidArgument) // the rate is fixed

	// Two goroutines at once: the second call fails rather than racing
	// inside the library. Finish decodes a whole clip pushed without Next,
	// which keeps the stream busy long enough.
	if err := s.Push(samples, rate); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error)
	go func() { finished <- s.Finish() }()
	time.Sleep(5 * time.Millisecond)
	err = s.Push(samples[:1600], rate)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err == nil || !strings.Contains(err.Error(), "another goroutine") {
		t.Errorf("Push during Finish on another goroutine = %v, want the in-use error", err)
	}

	wantKind(t, s.Push(samples[:1600], rate), engine.ErrInvalidArgument)
	wantKind(t, s.ForceEndpoint(), engine.ErrInvalidArgument)
	wantKind(t, s.Finish(), engine.ErrInvalidArgument)
	if r, err := s.Next(); err != nil || r == nil || !r.Final {
		t.Fatalf("Next after Finish = %v, %v", r, err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close = %v", err)
	}
	_, err = s.Next()
	wantKind(t, err, engine.ErrInvalidArgument)
}

func TestNemotronUnloadClosesStreams(t *testing.T) {
	samples, rate, _ := fixture(t)
	e, _ := loadNemotron(t, false)
	s, err := e.StartStream(context.Background(), engine.Options{InterimResults: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Push(samples[:rate], rate); err != nil {
		t.Fatal(err)
	}
	if err := e.Unload(); err != nil {
		t.Fatal(err)
	}
	wantKind(t, s.Push(samples[:rate], rate), engine.ErrInvalidArgument)
	_, err = s.Next()
	wantKind(t, err, engine.ErrInvalidArgument)
	if err := s.Close(); err != nil {
		t.Errorf("Close after Unload = %v", err)
	}
	_, err = e.StartStream(context.Background(), engine.Options{})
	wantKind(t, err, engine.ErrNotLoaded)
}

// Run with -race: streams on one recognizer, each driven by its own
// goroutine, alongside an offline recognition.
func TestNemotronConcurrentStreams(t *testing.T) {
	samples, rate, want := fixture(t)
	e, _ := loadNemotron(t, false)
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			run := streamClip(t, e, samples, rate, 40*time.Millisecond, 0, engine.Options{InterimResults: true})
			if w := wer(want, run.final.Text); w > maxWER {
				t.Errorf("concurrent stream: WER %.2f: %q", w, run.final.Text)
			}
		})
	}
	wg.Go(func() {
		r, err := e.Recognize(context.Background(), samples, rate, engine.Options{})
		if err != nil {
			t.Error(err)
		} else if w := wer(want, r.Text); w > maxWER {
			t.Errorf("concurrent Recognize: WER %.2f: %q", w, r.Text)
		}
	})
	wg.Wait()
}

func TestStartStreamBeforeLoad(t *testing.T) {
	_, err := New().StartStream(context.Background(), engine.Options{})
	wantKind(t, err, engine.ErrNotLoaded)
	_, err = New().Load(context.Background(), engine.LoadSpec{Path: "unused.gguf", Latency: "instant"})
	wantKind(t, err, engine.ErrInvalidArgument)
}

// Offline-only models (Parakeet TDT) refuse to stream with
// ErrInvalidArgument, which callers take as "batch only". The library itself
// reports a runtime error, so the binding checks first.
func TestTDTRefusesToStream(t *testing.T) {
	dir := os.Getenv("GOSCRIBER_TEST_MODELS")
	var path string
	for _, f := range []string{"parakeet-tdt-0.6b-v2.q8_0.gguf", "parakeet-tdt-0.6b-v3.q8_0.gguf"} {
		if _, err := os.Stat(filepath.Join(dir, f)); dir != "" && err == nil {
			path = filepath.Join(dir, f)
			break
		}
	}
	if path == "" {
		t.Skip("set GOSCRIBER_TEST_MODELS to a directory holding a Parakeet TDT GGUF (parakeet-tdt-0.6b-v2.q8_0.gguf or parakeet-tdt-0.6b-v3.q8_0.gguf) to run this test")
	}
	samples, rate, want := fixture(t)
	e := New()
	defer func() { _ = e.Unload() }()
	info, err := e.Load(context.Background(), engine.LoadSpec{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if info.Streaming || info.DecodeMode() != engine.DecodeOnce {
		t.Errorf("ModelInfo = %+v, want an offline model", info)
	}
	_, err = e.StartStream(context.Background(), engine.Options{InterimResults: true})
	wantKind(t, err, engine.ErrInvalidArgument)
	r, err := e.Recognize(context.Background(), samples, rate, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if w := wer(want, r.Text); w > maxWER {
		t.Errorf("TDT WER %.2f: %q", w, r.Text)
	}
}
