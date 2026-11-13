package autounload_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/autounload"
	"github.com/opragmatico/goscriber/internal/engine/fake"
)

const timeout = 30 * time.Second

var (
	spec  = engine.LoadSpec{ID: "nemotron", Path: "/models/nemotron.gguf", Latency: engine.LatencyLow}
	audio = make([]float32, 1600)
	quiet = slog.New(slog.DiscardHandler)
)

// loaded returns a fake engine that streams and an Engine over it with spec
// loaded.
func loaded(t *testing.T, script fake.Script, d time.Duration) (*fake.Engine, *autounload.Engine) {
	t.Helper()
	script.Model.Streaming = true
	inner := fake.New(script)
	e := autounload.New(inner, d, quiet)
	if _, err := e.Load(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	return inner, e
}

// sleep advances the bubble's clock by d and lets timers that fired run.
func sleep(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

func wantResident(t *testing.T, e *autounload.Engine, inner *fake.Engine, want bool) {
	t.Helper()
	if e.Resident() != want || inner.Loaded() != want {
		t.Fatalf("Resident() = %v, inner Loaded() = %v; want %v", e.Resident(), inner.Loaded(), want)
	}
}

func recognize(t *testing.T, e *autounload.Engine) {
	t.Helper()
	if _, err := e.Recognize(t.Context(), audio, 16000, engine.Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestIdleModelUnloadsAtTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner, e := loaded(t, fake.Script{}, timeout)

		sleep(timeout - time.Nanosecond)
		wantResident(t, e, inner, true)
		sleep(time.Nanosecond)
		wantResident(t, e, inner, false)
		if inner.Unloads() != 1 {
			t.Errorf("inner unloads = %d, want 1", inner.Unloads())
		}
		if info, ok := e.Model(); !ok || info.ID != spec.ID {
			t.Errorf("Model() = %+v, %v; an idle unload must keep the model selected", info, ok)
		}
	})
}

func TestNextCallReloadsTheSameSpec(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner, e := loaded(t, fake.Script{Replies: []fake.Reply{fake.Transcript("hello")}}, timeout)
		sleep(timeout)
		wantResident(t, e, inner, false)

		res, err := e.Recognize(t.Context(), audio, 16000, engine.Options{})
		if err != nil || res.Text != "hello" {
			t.Fatalf("Recognize after an idle unload = %+v, %v", res, err)
		}
		wantResident(t, e, inner, true)
		if loads := inner.Loads(); len(loads) != 2 || loads[1] != spec {
			t.Errorf("inner loads = %+v, want spec loaded twice", loads)
		}

		// The reloaded model goes idle again.
		sleep(timeout)
		wantResident(t, e, inner, false)
	})
}

func TestEveryKindOfCallReloads(t *testing.T) {
	calls := map[string]func(context.Context, *autounload.Engine) error{
		"recognize": func(ctx context.Context, e *autounload.Engine) error {
			_, err := e.Recognize(ctx, audio, 16000, engine.Options{})
			return err
		},
		"start stream": func(ctx context.Context, e *autounload.Engine) error {
			s, err := e.StartStream(ctx, engine.Options{})
			if err == nil {
				s.Close()
			}
			return err
		},
		"acquire": func(ctx context.Context, e *autounload.Engine) error {
			release, err := e.Acquire(ctx)
			if err == nil {
				release()
			}
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				inner, e := loaded(t, fake.Script{}, timeout)
				sleep(timeout)
				if err := call(t.Context(), e); err != nil {
					t.Fatal(err)
				}
				if len(inner.Loads()) != 2 {
					t.Errorf("inner loads = %d, want a reload", len(inner.Loads()))
				}
			})
		})
	}
}

func TestRecognitionPostponesUnload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner, e := loaded(t, fake.Script{Replies: []fake.Reply{{Delay: 2 * timeout}}}, timeout)

		sleep(timeout / 2)
		done := make(chan error)
		go func() {
			_, err := e.Recognize(context.Background(), audio, 16000, engine.Options{})
			done <- err
		}()
		sleep(timeout) // past the original deadline, mid-recognition
		wantResident(t, e, inner, true)
		if err := <-done; err != nil {
			t.Fatal(err)
		}

		// The idle time counts from the end of the recognition.
		sleep(timeout - time.Nanosecond)
		wantResident(t, e, inner, true)
		sleep(time.Nanosecond)
		wantResident(t, e, inner, false)
	})
}

func TestOpenStreamPostponesUnload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner, e := loaded(t, fake.Script{}, timeout)
		s, err := e.StartStream(t.Context(), engine.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Push(audio, 16000); err != nil {
			t.Fatal(err)
		}
		sleep(3 * timeout)
		wantResident(t, e, inner, true)

		s.Close()
		s.Close() // a second Close must not count the stream finished twice
		sleep(timeout)
		wantResident(t, e, inner, false)
	})
}

func TestFailedStartStreamIsNotBusy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner, e := loaded(t, fake.Script{Streams: []fake.StreamScript{{StartErr: engine.ErrResourceExhausted}}}, timeout)
		if _, err := e.StartStream(t.Context(), engine.Options{}); !errors.Is(err, engine.ErrResourceExhausted) {
			t.Fatalf("StartStream error = %v", err)
		}
		sleep(timeout)
		wantResident(t, e, inner, false)
	})
}

func TestAcquireHoldsTheModel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner, e := loaded(t, fake.Script{}, timeout)
		release, err := e.Acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		release2, err := e.Acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		sleep(10 * timeout) // a long recording
		wantResident(t, e, inner, true)

		release()
		release() // safe, and does not release the other hold
		sleep(timeout)
		wantResident(t, e, inner, true)

		release2()
		sleep(timeout)
		wantResident(t, e, inner, false)
	})
}

func TestNeverUnloads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner, e := loaded(t, fake.Script{}, autounload.Never)
		recognize(t, e)
		sleep(24 * time.Hour)
		wantResident(t, e, inner, true)
	})
}

func TestZeroTimeoutUnloadsAsSoonAsIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner, e := loaded(t, fake.Script{}, 0)
		synctest.Wait()
		wantResident(t, e, inner, false)

		release, err := e.Acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		sleep(time.Hour)
		wantResident(t, e, inner, true) // held, so kept
		release()
		synctest.Wait()
		wantResident(t, e, inner, false)
		if inner.Unloads() != 2 {
			t.Errorf("inner unloads = %d, want 2", inner.Unloads())
		}
	})
}

func TestSetTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner, e := loaded(t, fake.Script{}, autounload.Never)
		sleep(time.Hour)

		// Turning the timeout on counts from now, not from when the model
		// went idle.
		e.SetTimeout(timeout)
		sleep(timeout - time.Nanosecond)
		wantResident(t, e, inner, true)

		// Setting the same value keeps the idle time so far.
		e.SetTimeout(timeout)
		sleep(time.Nanosecond)
		wantResident(t, e, inner, false)

		// Turning it off cancels a pending unload. Any negative value means
		// never.
		recognize(t, e)
		e.SetTimeout(autounload.Never)
		e.SetTimeout(-time.Hour)
		sleep(time.Hour)
		wantResident(t, e, inner, true)

		// A shorter timeout replaces a longer pending one.
		e.SetTimeout(time.Hour)
		sleep(time.Minute)
		e.SetTimeout(time.Second)
		sleep(time.Second)
		wantResident(t, e, inner, false)
	})
}

func TestExplicitUnloadIsFinal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner, e := loaded(t, fake.Script{}, timeout)
		if err := e.Unload(); err != nil {
			t.Fatal(err)
		}
		sleep(2 * timeout) // the cancelled idle timer must not unload again
		if inner.Unloads() != 1 {
			t.Errorf("inner unloads = %d, want 1", inner.Unloads())
		}
		if _, ok := e.Model(); ok {
			t.Error("Model() reports a model after Unload")
		}
		if _, err := e.Recognize(t.Context(), audio, 16000, engine.Options{}); !errors.Is(err, engine.ErrNotLoaded) {
			t.Errorf("Recognize after Unload: err = %v, want ErrNotLoaded", err)
		}
		if _, err := e.Acquire(t.Context()); !errors.Is(err, engine.ErrNotLoaded) {
			t.Errorf("Acquire after Unload: err = %v, want ErrNotLoaded", err)
		}
		if len(inner.Loads()) != 1 {
			t.Errorf("inner loads = %d; Unload must not lead to a reload", len(inner.Loads()))
		}
	})
}

func TestNothingLoadedFails(t *testing.T) {
	e := autounload.New(fake.New(fake.Script{}), timeout, quiet)
	if _, err := e.Recognize(t.Context(), audio, 16000, engine.Options{}); !errors.Is(err, engine.ErrNotLoaded) {
		t.Errorf("err = %v, want ErrNotLoaded", err)
	}
	if _, err := e.StartStream(t.Context(), engine.Options{}); !errors.Is(err, engine.ErrNotLoaded) {
		t.Errorf("err = %v, want ErrNotLoaded", err)
	}
	if err := e.Unload(); err != nil {
		t.Errorf("Unload with nothing loaded: %v", err)
	}
}

func TestFailedLoadForgetsTheModel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := &flaky{Engine: fake.New(fake.Script{}), fail: map[int32]bool{2: true}}
		e := autounload.New(inner, timeout, quiet)
		if _, err := e.Load(t.Context(), spec); err != nil {
			t.Fatal(err)
		}
		// A Load that fails leaves nothing loaded, as in the inner engine.
		if _, err := e.Load(t.Context(), spec); !errors.Is(err, engine.ErrOutOfMemory) {
			t.Fatalf("Load error = %v", err)
		}
		if _, ok := e.Model(); ok || e.Resident() {
			t.Error("a failed Load left a model selected")
		}
		if _, err := e.Recognize(t.Context(), audio, 16000, engine.Options{}); !errors.Is(err, engine.ErrNotLoaded) {
			t.Errorf("err = %v, want ErrNotLoaded", err)
		}
		sleep(2 * timeout) // no idle timer is left behind
		if len(inner.Loads()) != 1 {
			t.Errorf("inner loads = %d; nothing may reload after a failed Load", len(inner.Loads()))
		}
	})
}

// flaky fails the Loads whose 1-based number is in fail.
type flaky struct {
	*fake.Engine
	n    atomic.Int32
	fail map[int32]bool
}

func (f *flaky) Load(ctx context.Context, spec engine.LoadSpec) (engine.ModelInfo, error) {
	if f.fail[f.n.Add(1)] {
		return engine.ModelInfo{}, &engine.Error{Op: "load", Kind: engine.ErrOutOfMemory}
	}
	return f.Engine.Load(ctx, spec)
}

func TestFailedReloadIsRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := &flaky{Engine: fake.New(fake.Script{}), fail: map[int32]bool{2: true}}
		e := autounload.New(inner, timeout, quiet)
		if _, err := e.Load(t.Context(), spec); err != nil {
			t.Fatal(err)
		}
		sleep(timeout)

		if _, err := e.Recognize(t.Context(), audio, 16000, engine.Options{}); !errors.Is(err, engine.ErrOutOfMemory) {
			t.Fatalf("first call after the unload: err = %v, want the reload's error", err)
		}
		if _, ok := e.Model(); !ok || e.Resident() {
			t.Fatal("a failed reload must keep the model selected but not resident")
		}
		recognize(t, e) // the next call tries again
		if !e.Resident() {
			t.Error("the retried reload did not load the model")
		}
	})
}

func TestCallerWaitingOnAReloadCanGiveUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := fake.New(fake.Script{LoadDelay: 10 * time.Second})
		e := autounload.New(inner, timeout, quiet)
		if _, err := e.Load(t.Context(), spec); err != nil {
			t.Fatal(err)
		}
		sleep(timeout)

		reloaded := make(chan struct{})
		go func() { // starts the slow reload
			defer close(reloaded)
			if _, err := e.Recognize(context.Background(), audio, 16000, engine.Options{}); err != nil {
				t.Error(err)
			}
		}()
		synctest.Wait()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		_, err := e.Recognize(ctx, audio, 16000, engine.Options{})
		if !errors.Is(err, engine.ErrCancelled) || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want a cancellation", err)
		}
		if waited := time.Since(start); waited != time.Second {
			t.Errorf("gave up after %v, want 1s", waited)
		}
		<-reloaded
	})
}

func TestIdleUnloadAndReloadAreLogged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var out lockedBuffer
		inner := fake.New(fake.Script{})
		e := autounload.New(inner, timeout, slog.New(slog.NewTextHandler(&out, nil)))
		if _, err := e.Load(t.Context(), spec); err != nil {
			t.Fatal(err)
		}
		sleep(timeout)
		recognize(t, e)

		log := out.String()
		for _, want := range []string{
			`msg="model unloaded after being idle" model=nemotron idle=30s`,
			`msg="model reloaded after an idle unload" model=nemotron`,
		} {
			if !strings.Contains(log, want) {
				t.Errorf("log lacks %q:\n%s", want, log)
			}
		}
	})
}

// TestConcurrentCallsAcrossIdleUnloads runs on the real clock with a tiny
// timeout, so the race detector sees idle unloads interleave with calls.
func TestConcurrentCallsAcrossIdleUnloads(t *testing.T) {
	inner := fake.New(fake.Script{Model: engine.ModelInfo{Streaming: true}})
	e := autounload.New(inner, time.Millisecond, quiet)
	if _, err := e.Load(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 50 {
				switch (g + i) % 3 {
				case 0:
					if _, err := e.Recognize(t.Context(), audio, 16000, engine.Options{}); err != nil {
						t.Error(err)
					}
				case 1:
					s, err := e.StartStream(t.Context(), engine.Options{})
					if err != nil {
						t.Error(err)
						continue
					}
					if err := s.Push(audio, 16000); err != nil {
						t.Error(err)
					}
					s.Close()
				case 2:
					release, err := e.Acquire(t.Context())
					if err != nil {
						t.Error(err)
						continue
					}
					release()
				}
				if i%10 == 0 {
					time.Sleep(2 * time.Millisecond) // let the model go idle
				}
			}
		})
	}
	wg.Wait()
	if inner.Unloads() == 0 {
		t.Error("the model never unloaded; the test did not exercise idle unloads")
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
