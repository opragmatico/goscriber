//go:build cgo && nemo

package nemo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/engine"
)

// maxWER is the word error rate the fixture must stay under.
const maxWER = 0.10

// nemotronFile is the model the gated tests load from GOSCRIBER_TEST_MODELS:
// NVIDIA's Q8_0 GGUF of nvidia/nemotron-3.5-asr-streaming-0.6b.
const nemotronFile = "nemotron-3.5-asr-streaming-0.6b.q8_0.gguf"

// testModel returns the path of file in the GOSCRIBER_TEST_MODELS directory,
// skipping the test when the variable is unset or the file is missing.
func testModel(t testing.TB, file string) string {
	t.Helper()
	dir := os.Getenv("GOSCRIBER_TEST_MODELS")
	if dir == "" {
		t.Skipf("set GOSCRIBER_TEST_MODELS to a directory holding %s to run this test", file)
	}
	path := filepath.Join(dir, file)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s: %v", file, err)
	}
	return path
}

func wantKind(t *testing.T, err, kind error) {
	t.Helper()
	if !errors.Is(err, kind) {
		t.Fatalf("err = %v, want %v", err, kind)
	}
	if _, ok := errors.AsType[*engine.Error](err); !ok {
		t.Fatalf("err = %T, want *engine.Error", err)
	}
}

func TestLoadMissingModel(t *testing.T) {
	e := New()
	path := filepath.Join(t.TempDir(), nemotronFile)
	_, err := e.Load(context.Background(), engine.LoadSpec{ID: "nemotron", Path: path})
	wantKind(t, err, engine.ErrModelNotFound)
	if got, _ := errors.AsType[*engine.Error](err); got.Detail != path {
		t.Errorf("Detail = %q, want the missing path", got.Detail)
	}

	vad := filepath.Join(t.TempDir(), "silero.gguf")
	model := (&ggufWriter{}).String("general.architecture", "asr").file(t)
	_, err = e.Load(context.Background(), engine.LoadSpec{Path: model, VADPath: vad})
	wantKind(t, err, engine.ErrModelNotFound)
}

func TestCallsBeforeLoad(t *testing.T) {
	e := New()
	_, err := e.Recognize(context.Background(), make([]float32, 1600), 0, engine.Options{})
	wantKind(t, err, engine.ErrNotLoaded)
	if err := e.Unload(); err != nil {
		t.Errorf("Unload with nothing loaded = %v", err)
	}
}

// A file that passes the binding's checks but not the library's: the error
// must carry the library's own message, copied in the failing C call.
func TestLoadCopiesTheLibraryError(t *testing.T) {
	model := (&ggufWriter{}).String("general.architecture", "asr").file(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			_, err := New().Load(context.Background(), engine.LoadSpec{Path: model, CPU: true})
			e, ok := errors.AsType[*engine.Error](err)
			if !ok || e.Op != "load" || e.Detail == "" {
				t.Errorf("Load of an empty GGUF = %v, want an *engine.Error with the library's message", err)
			}
		})
	}
	wg.Wait()
}

func TestLoadCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().Load(ctx, engine.LoadSpec{Path: "unused.gguf"})
	wantKind(t, err, engine.ErrCancelled)
}

func loadNemotron(t testing.TB, cpu bool) (*Engine, engine.ModelInfo) {
	t.Helper()
	path := testModel(t, nemotronFile)
	e := New()
	start := time.Now()
	info, err := e.Load(context.Background(), engine.LoadSpec{ID: "nemotron-3.5", Path: path, CPU: cpu})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("loaded in %v (cpu=%v)", time.Since(start).Round(time.Millisecond), cpu)
	t.Cleanup(func() { _ = e.Unload() })
	return e, info
}

func TestNemotronModelInfo(t *testing.T) {
	_, info := loadNemotron(t, false)
	if info.ID != "nemotron-3.5" || !info.Streaming || info.DecodeMode() != engine.DecodeLive {
		t.Errorf("ModelInfo = %+v", info)
	}
	for _, lang := range []string{"auto", "en-US", "es-ES", "de-DE"} {
		if !slices.Contains(info.Languages, lang) {
			t.Errorf("Languages lack %q: %v", lang, info.Languages)
		}
	}
	t.Logf("%d languages: %v", len(info.Languages), info.Languages)
}

func TestNemotronRecognizesFixture(t *testing.T) {
	samples, rate, want := fixture(t)
	clip := time.Duration(len(samples)) * time.Second / time.Duration(rate)
	for _, backend := range []struct {
		name string
		cpu  bool
	}{{"metal", false}, {"cpu", true}} {
		t.Run(backend.name, func(t *testing.T) {
			e, _ := loadNemotron(t, backend.cpu)
			opts := engine.Options{Language: "en-US", WordTimestamps: true}
			start := time.Now()
			r, err := e.Recognize(context.Background(), samples, rate, opts)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("decoded %v of audio in %v: %q", clip, time.Since(start).Round(time.Millisecond), r.Text)

			if w := wer(want, r.Text); w > maxWER {
				t.Errorf("WER %.2f > %.2f:\n got %q\nwant %q", w, maxWER, r.Text, want)
			}
			if !strings.Contains(r.Text, "Americans") || !strings.ContainsAny(r.Text, ",.") {
				t.Errorf("transcript %q lost the model's punctuation and casing", r.Text)
			}
			if !r.Final || r.AudioProcessed < clip-100*time.Millisecond || r.AudioProcessed > clip+100*time.Millisecond {
				t.Errorf("Final = %v, AudioProcessed = %v; want a final over %v", r.Final, r.AudioProcessed, clip)
			}
			if len(r.Words) != len(strings.Fields(r.Text)) {
				t.Errorf("%d words for %q", len(r.Words), r.Text)
			}
			var last time.Duration
			for _, w := range r.Words {
				if w.Text == "" || w.Start < last || w.End < w.Start || w.End > clip {
					t.Errorf("word %+v out of order or outside the clip", w)
				}
				last = w.Start
			}
			if !slices.Contains(r.Languages, "en-US") {
				t.Errorf("Languages = %v, want en-US", r.Languages)
			}
		})
	}
}

func TestNemotronResamplesAndDetectsLanguage(t *testing.T) {
	samples, rate, want := fixture(t)
	e, _ := loadNemotron(t, false)

	// The same clip at 48 kHz, by repeating samples: the engine resamples it.
	up := make([]float32, 0, 3*len(samples))
	for _, s := range samples {
		up = append(up, s, s, s)
	}
	r, err := e.Recognize(context.Background(), up, 3*rate, engine.Options{Language: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if w := wer(want, r.Text); w > maxWER {
		t.Errorf("48 kHz, auto: WER %.2f: %q", w, r.Text)
	}
	if len(r.Words) != 0 {
		t.Errorf("%d words without WordTimestamps", len(r.Words))
	}
	t.Logf("auto-detected %v", r.Languages)
}

func TestNemotronRecognizeErrors(t *testing.T) {
	samples, _, _ := fixture(t)
	e, _ := loadNemotron(t, false)

	_, err := e.Recognize(context.Background(), nil, 0, engine.Options{})
	wantKind(t, err, engine.ErrInvalidArgument)
	_, err = e.Recognize(context.Background(), samples, 4000, engine.Options{})
	wantKind(t, err, engine.ErrInvalidArgument)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = e.Recognize(ctx, samples, 0, engine.Options{})
	wantKind(t, err, engine.ErrCancelled)

	// Cancelled mid-decode: the decode finishes, and its result is dropped.
	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(time.Millisecond, cancel)
	_, err = e.Recognize(ctx, samples, 0, engine.Options{})
	wantKind(t, err, engine.ErrCancelled)
}

// Run with -race: one recognizer serves several goroutines at once.
func TestNemotronConcurrentRecognize(t *testing.T) {
	samples, rate, want := fixture(t)
	e, _ := loadNemotron(t, false)

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 2 {
				r, err := e.Recognize(context.Background(), samples, rate, engine.Options{WordTimestamps: true})
				if err != nil {
					t.Error(err)
					return
				}
				if w := wer(want, r.Text); w > maxWER {
					t.Errorf("concurrent WER %.2f: %q", w, r.Text)
				}
			}
		})
	}
	wg.Wait()
}

// Unload waits for the calls in flight instead of freeing the recognizer
// under them; calls that start after it find no model.
func TestNemotronUnloadWaitsForCallsInFlight(t *testing.T) {
	samples, rate, _ := fixture(t)
	e, _ := loadNemotron(t, false)

	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for range 4 {
		wg.Go(func() {
			_, err := e.Recognize(context.Background(), samples, rate, engine.Options{})
			switch {
			case err == nil:
				mu.Lock()
				done++
				mu.Unlock()
			case !errors.Is(err, engine.ErrNotLoaded):
				t.Errorf("Recognize during Unload = %v", err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond) // let the calls start decoding
	if err := e.Unload(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if done == 0 {
		t.Error("no call in flight completed: Unload did not wait for them")
	}
	t.Logf("%d of 4 calls were in flight and completed", done)
	_, err := e.Recognize(context.Background(), samples, rate, engine.Options{})
	wantKind(t, err, engine.ErrNotLoaded)
}

// rssKB returns the process's resident set size in KiB.
func rssKB(t testing.TB) int {
	t.Helper()
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	kb, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("ps rss %q: %v", out, err)
	}
	return kb
}

// footprintKB returns the process's physical footprint in KiB on macOS: its
// dirty and compressed memory, which is what the system charges it for and
// Activity Monitor shows. RSS also counts clean pages the malloc zones keep
// mapped for reuse, which the system reclaims at will.
func footprintKB(t testing.TB) int {
	t.Helper()
	out, err := exec.Command("footprint", "-f", "bytes", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Fatalf("footprint: %v", err)
	}
	_, rest, ok := strings.Cut(string(out), "Footprint: ")
	if !ok {
		t.Fatalf("footprint output %q", out)
	}
	b, err := strconv.Atoi(strings.Fields(rest)[0])
	if err != nil {
		t.Fatalf("footprint output %q: %v", out, err)
	}
	return b / 1024
}

// memKB is the memory measure the leak test holds steady: the physical
// footprint on macOS, the RSS elsewhere.
func memKB(t testing.TB) int {
	if runtime.GOOS == "darwin" {
		return footprintKB(t)
	}
	return rssKB(t)
}

// Load → recognize → unload, over and over, must not leak: native memory
// returns to the OS on Unload. GOSCRIBER_TEST_CYCLES overrides the count.
func TestNemotronLoadCyclesDoNotLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 100 model loads")
	}
	path := testModel(t, nemotronFile)
	samples, rate, want := fixture(t)
	cycles := 100
	if n, err := strconv.Atoi(os.Getenv("GOSCRIBER_TEST_CYCLES")); err == nil && n > 0 {
		cycles = n
	}

	e := New()
	cycle := func() {
		if _, err := e.Load(context.Background(), engine.LoadSpec{Path: path}); err != nil {
			t.Fatal(err)
		}
		r, err := e.Recognize(context.Background(), samples, rate, engine.Options{WordTimestamps: true})
		if err != nil {
			t.Fatal(err)
		}
		if w := wer(want, r.Text); w > maxWER {
			t.Fatalf("WER %.2f: %q", w, r.Text)
		}
		if err := e.Unload(); err != nil {
			t.Fatal(err)
		}
	}

	// Warm up first: the Metal device, the allocator and ggml keep some
	// process-wide state after the first loads.
	const warmup = 5
	for range warmup {
		cycle()
	}
	runtime.GC()
	base, baseRSS := memKB(t), rssKB(t)
	start := time.Now()
	for i := range cycles {
		cycle()
		if (i+1)%25 == 0 || i+1 == cycles {
			runtime.GC()
			t.Logf("after %3d cycles: memory %+6.1f MiB, RSS %+6.1f MiB",
				i+1, float64(memKB(t)-base)/1024, float64(rssKB(t)-baseRSS)/1024)
		}
	}
	runtime.GC()
	growth := memKB(t) - base
	t.Logf("%d cycles in %v; after warm-up: memory %.1f MiB, RSS %.1f MiB",
		cycles, time.Since(start).Round(time.Second), float64(base)/1024, float64(baseRSS)/1024)
	// A leaked recognizer would add hundreds of MiB per cycle.
	if growth > 32*1024 {
		t.Errorf("memory grew by %.1f MiB over %d cycles", float64(growth)/1024, cycles)
	}
}
