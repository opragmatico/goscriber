//go:build cgo && nemo

package nemo

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/engine"
	"github.com/opragmatico/goscriber/internal/engine/autounload"
	"github.com/opragmatico/goscriber/internal/engine/warmup"
)

// maxFirstDictation is how much longer than later ones the first dictation
// after a warmed-up load may take, on Apple Silicon.
const maxFirstDictation = 1.10

// longestChunk is pipeline.LongestChunk, which the app warms offline models
// up on.
const longestChunk = 25 * time.Second

// The model lifecycle as the app runs it (issue #29), with the engine in
// this process: the idle unloader over the warm-up over the binding, the
// Silero VAD loaded when it is there. The first dictation after a load
// takes no more than 10 % longer than later ones, Nemotron streamed in
// 20 ms chunks and Parakeet decoded whole; once the model is unloaded for
// being idle, the process's memory is back where it was before the load.
func TestModelLifecycle(t *testing.T) {
	samples, rate, want := fixture(t)
	vad := ""
	if dir := os.Getenv("GOSCRIBER_TEST_MODELS"); dir != "" {
		if p := filepath.Join(dir, vadFile); fileExists(p) {
			vad = p
		}
	}
	log := slog.New(slog.DiscardHandler)
	for _, file := range speechModels {
		t.Run(file, func(t *testing.T) {
			path := testModel(t, file)
			spec := engine.LoadSpec{ID: file, Path: path, VADPath: vad}

			e := autounload.New(warmup.New(New(), warmup.Options{Longest: longestChunk, Log: log}), autounload.Never, log)
			var info engine.ModelInfo
			dictate := func() time.Duration {
				start := time.Now()
				var text string
				if info.Streaming {
					text = streamClip(t, e, samples, rate, 20*time.Millisecond, 0, engine.Options{Language: "en-US", InterimResults: true}).final.Text
				} else {
					r, err := e.Recognize(context.Background(), samples, rate, engine.Options{})
					if err != nil {
						t.Fatal(err)
					}
					text = r.Text
				}
				if w := wer(want, text); w > maxWER {
					t.Fatalf("WER %.2f: %q", w, text)
				}
				return time.Since(start)
			}

			start := time.Now()
			var err error
			if info, err = e.Load(context.Background(), spec); err != nil {
				t.Fatal(err)
			}
			load := time.Since(start)
			first := dictate()
			var later []time.Duration
			for range 5 {
				later = append(later, dictate())
			}
			slices.Sort(later)
			steady := later[len(later)/2]
			ratio := float64(first) / float64(steady)
			t.Logf("load and warm-up %v; first dictation %v, later ones %v (median), %.2f×",
				load.Round(time.Millisecond), first.Round(100*time.Microsecond), steady.Round(100*time.Microsecond), ratio)
			if !raceEnabled && runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" && ratio > maxFirstDictation {
				t.Errorf("the first dictation took %.2f× as long as later ones, want at most %.2f×", ratio, maxFirstDictation)
			}

			// The Metal device, its compiled pipelines and ggml keep some
			// process-wide state after the first load and dictation, which is
			// not the model's: the baseline comes after them.
			if err := e.Unload(); err != nil {
				t.Fatal(err)
			}
			runtime.GC()
			base := memKB(t)
			if info, err = e.Load(context.Background(), spec); err != nil {
				t.Fatal(err)
			}
			dictate()
			loaded := memKB(t)
			e.SetTimeout(0)
			deadline := time.Now().Add(5 * time.Second)
			for e.Resident() {
				if time.Now().After(deadline) {
					t.Fatal("the idle model was not unloaded")
				}
				time.Sleep(10 * time.Millisecond)
			}
			runtime.GC()
			after := memKB(t)
			t.Logf("memory %+.1f MiB loaded, %+.1f MiB after the idle unload", float64(loaded-base)/1024, float64(after-base)/1024)
			// A model still loaded would hold hundreds of MiB.
			if after-base > 32*1024 {
				t.Errorf("memory is %.1f MiB above where it was before the load", float64(after-base)/1024)
			}
			if err := e.Unload(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
