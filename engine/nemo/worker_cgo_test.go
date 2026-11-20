//go:build cgo && nemo

package nemo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/engine"
	"github.com/opragmatico/goscriber/internal/engine/worker"
)

// envWorker makes the test binary an engine worker hosting this binding,
// as `goscriber engine-worker` does, so the tests can measure the worker
// without building the app.
const envWorker = "GOSCRIBER_NEMO_TEST_WORKER"

func TestMain(m *testing.M) {
	if os.Getenv(envWorker) != "" {
		if err := worker.Run(New()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// newSupervisor runs the binding in a worker: this test binary.
func newSupervisor(t testing.TB) *worker.Supervisor {
	t.Helper()
	s := worker.New(worker.Options{Command: func() (*exec.Cmd, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), envWorker+"=1", "GORACE=atexit_sleep_ms=0 "+os.Getenv("GORACE"))
		return cmd, nil
	}})
	t.Cleanup(func() { _ = s.Unload() })
	return s
}

// TestWorkerIPCOverhead measures what the worker process adds to a
// dictation: Recognize of a 10 s clip and streaming the same clip in 10 ms
// pushes, each through the binding in this process and through a worker,
// on Metal. Runs alternate between the two so they share any drift; the
// overhead is the difference of their medians. Issue #28 asks for less
// than 5 ms on the 10 s clip.
func TestWorkerIPCOverhead(t *testing.T) {
	models := []struct {
		name, file string
		stream     bool
	}{
		{"Parakeet TDT 0.6B v2", "parakeet-tdt-0.6b-v2.q8_0.gguf", false},
		{"Nemotron Streaming 3.5", nemotronFile, true},
	}
	samples, rate, _ := fixture(t)
	clip := samples[:10*rate]
	const runs = 15
	for _, m := range models {
		t.Run(m.file, func(t *testing.T) {
			spec := engine.LoadSpec{ID: m.file, Path: testModel(t, m.file)}
			local := New()
			if _, err := local.Load(context.Background(), spec); err != nil {
				t.Fatal(err)
			}
			defer local.Unload()
			remote := newSupervisor(t)
			if _, err := remote.Load(context.Background(), spec); err != nil {
				t.Fatal(err)
			}

			recognize := func(e engine.Engine) time.Duration {
				start := time.Now()
				res, err := e.Recognize(context.Background(), clip, rate, engine.Options{})
				if err != nil {
					t.Fatal(err)
				}
				if res.Text == "" {
					t.Fatal("empty transcript")
				}
				return time.Since(start)
			}
			// stream pushes the clip in 10 ms frames, pulling every result
			// after each, as the coordinator does, then finishes.
			stream := func(e engine.Engine) time.Duration {
				start := time.Now()
				s, err := e.StartStream(context.Background(), engine.Options{InterimResults: true})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				frame := rate / 100
				for off := 0; off < len(clip); off += frame {
					if err := s.Push(clip[off:min(off+frame, len(clip))], rate); err != nil {
						t.Fatal(err)
					}
					for {
						r, err := s.Next()
						if err != nil {
							t.Fatal(err)
						}
						if r == nil {
							break
						}
					}
				}
				if err := s.Finish(); err != nil {
					t.Fatal(err)
				}
				for {
					if _, err := s.Next(); err != nil {
						break
					}
				}
				return time.Since(start)
			}

			// hop pushes the whole clip into a stream in one call: the audio
			// crosses the pipe, but Push only buffers it, so this is the hop
			// alone, without the decode's own variation.
			hop := func(e engine.Engine) time.Duration {
				s, err := e.StartStream(context.Background(), engine.Options{})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				start := time.Now()
				if err := s.Push(clip, rate); err != nil {
					t.Fatal(err)
				}
				return time.Since(start)
			}

			// measure returns the overhead as the difference of the
			// minimums, which the GPU's run-to-run variation (a few
			// milliseconds) disturbs least, and logs the medians too.
			measure := func(what string, run func(engine.Engine) time.Duration) time.Duration {
				run(local) // warm up both: the first decode builds its compute graph
				run(remote)
				var in, out []time.Duration
				for range runs {
					in = append(in, run(local))
					out = append(out, run(remote))
				}
				r := func(d time.Duration) time.Duration { return d.Round(10 * time.Microsecond) }
				t.Logf("%s, %s (%d runs): in process min %v / median %v; through the worker min %v / median %v; overhead %v",
					m.name, what, runs, r(slices.Min(in)), r(median(in)), r(slices.Min(out)), r(median(out)),
					r(slices.Min(out)-slices.Min(in)))
				return slices.Min(out) - slices.Min(in)
			}
			if overhead := measure("Recognize of 10 s", recognize); overhead > 5*time.Millisecond && !raceEnabled {
				t.Errorf("the worker adds %v to Recognize of a 10 s clip, want under 5 ms", overhead)
			}
			if m.stream {
				if overhead := measure("pushing 10 s in one call", hop); overhead > 5*time.Millisecond && !raceEnabled {
					t.Errorf("sending a 10 s clip to the worker takes %v, want under 5 ms", overhead)
				}
				// 1000 pushes and at least as many Nexts: two round trips per
				// 10 ms of audio.
				overhead := measure("streaming 10 s in 10 ms pushes", stream)
				t.Logf("%s: %v per 10 ms frame", m.name, (overhead / 1000).Round(time.Microsecond))
			}
		})
	}
}

func median(d []time.Duration) time.Duration {
	s := slices.Clone(d)
	slices.Sort(s)
	return s[len(s)/2]
}
