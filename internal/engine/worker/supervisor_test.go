package worker

import (
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/engine"
	"github.com/opragmatico/goscriber/internal/engine/autounload"
	"github.com/opragmatico/goscriber/internal/notice"
)

func TestRecognizeThroughAWorker(t *testing.T) {
	h := newHarness(t, harnessOptions{script: "batch"})
	info := h.load(t, false)
	if info.ID != "fake" || info.Streaming {
		t.Errorf("ModelInfo = %+v, want the fake offline model", info)
	}
	p := h.worker()
	res, err := h.Recognize(context.Background(), speech(16000), 16000, engine.Options{Language: "en-US", Phrases: []string{"Kowalczyk"}})
	if err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	if res.Text != batchText || !res.Final || res.AudioProcessed != time.Second {
		t.Errorf("Result = %+v, want %q, final, 1 s", res, batchText)
	}
	if h.worker() != p {
		t.Error("the worker changed between calls")
	}
	if err := h.Unload(); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	waitDead(t, p)
	if h.worker() != nil {
		t.Error("a worker is still held after Unload")
	}
	_, err = h.Recognize(context.Background(), speech(1600), 16000, engine.Options{})
	wantKind(t, err, engine.ErrNotLoaded)
	wantCodes(t, h.notices)
}

func TestStreamThroughAWorker(t *testing.T) {
	h := newHarness(t, harnessOptions{script: "stream"})
	info := h.load(t, false)
	if !info.Streaming || !slices.Equal(info.Languages, []string{"auto", "en-US"}) {
		t.Errorf("ModelInfo = %+v, want the fake streaming model", info)
	}
	s, err := h.StartStream(context.Background(), engine.Options{InterimResults: true})
	if err != nil {
		t.Fatalf("StartStream: %v", err)
	}
	var interims []string
	for range 60 { // 600 ms in 10 ms pushes
		if err := s.Push(speech(160), 16000); err != nil {
			t.Fatalf("Push: %v", err)
		}
		for {
			r, err := s.Next()
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if r == nil {
				break
			}
			interims = append(interims, r.Text)
		}
	}
	want := []string{"and", "and so", "and so my", "and so my fellow", "and so my fellow americans"}
	if !slices.Equal(interims, want) {
		t.Errorf("interims = %q, want %q", interims, want)
	}
	if err := s.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	r, err := s.Next()
	if err != nil || r == nil || !r.Final || r.Text != streamText {
		t.Fatalf("final = %+v, %v; want %q", r, err, streamText)
	}
	if _, err := s.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("Next after the final = %v, want io.EOF", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if err := s.Push(speech(160), 16000); !errors.Is(err, engine.ErrInvalidArgument) {
		t.Errorf("Push after Close = %v, want ErrInvalidArgument", err)
	}
}

func TestEngineErrorsPassThrough(t *testing.T) {
	h := newHarness(t, harnessOptions{script: "load-error"})
	_, err := h.Load(context.Background(), engine.LoadSpec{ID: "fake", Path: "missing.gguf"})
	wantKind(t, err, engine.ErrModelNotFound)
	if h.worker() != nil {
		t.Error("a worker is held after a failed load")
	}

	h = newHarness(t, harnessOptions{script: "batch"})
	h.load(t, false)
	_, err = h.Recognize(context.Background(), nil, 16000, engine.Options{})
	wantKind(t, err, engine.ErrInvalidArgument)
	_, err = h.Recognize(context.Background(), speech(100), 4000, engine.Options{})
	wantKind(t, err, engine.ErrInvalidArgument)
	_, err = h.StartStream(context.Background(), engine.Options{})
	wantKind(t, err, engine.ErrInvalidArgument) // an offline model cannot stream
	wantCodes(t, h.notices)
}

// A worker that crashes during a decode is replaced and the decode retried;
// on the GPU, the new worker runs on the CPU.
func TestCrashDuringRecognizeIsRetried(t *testing.T) {
	for _, cpu := range []bool{false, true} {
		t.Run("cpu="+strconv.FormatBool(cpu), func(t *testing.T) {
			h := newHarness(t, harnessOptions{script: "batch", delay: 300 * time.Millisecond})
			h.load(t, cpu)
			first := h.worker()

			type outcome struct {
				res engine.Result
				err error
			}
			done := make(chan outcome, 1)
			go func() {
				res, err := h.Recognize(context.Background(), speech(16000), 16000, engine.Options{})
				done <- outcome{res, err}
			}()
			waitBusy(t, first, opRecognize)
			h.kill(t)
			got := <-done
			if got.err != nil || got.res.Text != batchText {
				t.Fatalf("Recognize = %q, %v; want %q from the retry", got.res.Text, got.err, batchText)
			}
			second := h.worker()
			if second == nil || second == first {
				t.Fatal("no new worker after the crash")
			}
			if !second.cpu {
				t.Error("the new worker runs on the GPU")
			}
			if cpu {
				wantCodes(t, h.notices, notice.CodeEngineCrashed)
			} else {
				wantCodes(t, h.notices, notice.CodeEngineCPUFallback)
			}
			if msg := h.notices.messages()[0]; !strings.Contains(msg, "while transcribing") {
				t.Errorf("notice = %q, want it to say what crashed", msg)
			}
		})
	}
}

// A stream does not survive its worker: its calls fail, and the next call
// starts a new worker.
func TestStreamLostWithItsWorker(t *testing.T) {
	h := newHarness(t, harnessOptions{script: "stream"})
	h.load(t, true)
	s, err := h.StartStream(context.Background(), engine.Options{InterimResults: true})
	if err != nil {
		t.Fatalf("StartStream: %v", err)
	}
	if err := s.Push(speech(1600), 16000); err != nil {
		t.Fatalf("Push: %v", err)
	}
	h.kill(t)
	err = s.Push(speech(160), 16000)
	wantKind(t, err, engine.ErrUnavailable, ErrWorkerLost)
	_, err = s.Next()
	wantKind(t, err, engine.ErrUnavailable, ErrWorkerLost)
	if err := s.Close(); err != nil {
		t.Errorf("Close of a lost stream = %v, want nil", err)
	}
	wantCodes(t, h.notices, notice.CodeEngineCrashed)

	res, err := h.Recognize(context.Background(), speech(16000), 16000, engine.Options{})
	if err != nil || res.Text != wholeText {
		t.Fatalf("Recognize after the crash = %q, %v; want %q", res.Text, err, wholeText)
	}
}

// A worker that misses its decode's timeout on the GPU is ended and the
// decode retried on the CPU; a hang there is not retried.
func TestHungDecode(t *testing.T) {
	h := newHarness(t, harnessOptions{
		script:   "batch",
		delay:    2 * time.Second,
		timeouts: Timeouts{Recognize: 100 * time.Millisecond, AudioFactor: 0.01, Heartbeat: time.Hour},
	})
	h.load(t, false)
	start := time.Now()
	_, err := h.Recognize(context.Background(), speech(1600), 16000, engine.Options{})
	wantKind(t, err, engine.ErrUnavailable, ErrWorkerLost)
	if !strings.Contains(err.Error(), "stopped responding") {
		t.Errorf("err = %v, want it to say the worker hung", err)
	}
	// 100 ms on the GPU, then 400 ms on the CPU, plus two worker starts.
	if took := time.Since(start); took > 1900*time.Millisecond {
		t.Errorf("Recognize took %v, want it to give up before the 2 s decode ends", took)
	}
	wantCodes(t, h.notices, notice.CodeEngineCPUFallback, notice.CodeEngineCrashed)
}

func TestCancelEndsALongDecode(t *testing.T) {
	h := newHarness(t, harnessOptions{script: "batch", delay: 5 * time.Second, timeouts: Timeouts{CancelGrace: 50 * time.Millisecond}})
	h.load(t, false)
	p := h.worker()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := h.Recognize(ctx, speech(16000), 16000, engine.Options{})
	wantKind(t, err, engine.ErrCancelled, context.DeadlineExceeded)
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("a cancelled Recognize took %v", took)
	}
	waitDead(t, p)
	wantCodes(t, h.notices) // ended on purpose: no crash

	// The next call starts a new worker; a short decode cancelled within the
	// grace keeps it.
	h2 := newHarness(t, harnessOptions{script: "batch", delay: 50 * time.Millisecond, timeouts: Timeouts{CancelGrace: 5 * time.Second}})
	h2.load(t, false)
	p = h2.worker()
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = h2.Recognize(ctx, speech(16000), 16000, engine.Options{})
	wantKind(t, err, engine.ErrCancelled)
	if !p.alive() || h2.worker() != p {
		t.Error("a short cancelled decode ended its worker")
	}
}

func TestGPUCrashWhileLoadingFallsBackToTheCPU(t *testing.T) {
	h := newHarness(t, harnessOptions{script: "gpu-crash"})
	h.load(t, false)
	if p := h.worker(); p == nil || !p.cpu {
		t.Fatal("the model is not loaded in a CPU worker")
	}
	wantCodes(t, h.notices, notice.CodeEngineCPUFallback)
	if msg := h.notices.messages()[0]; !strings.Contains(msg, "while loading the model on the GPU") {
		t.Errorf("notice = %q", msg)
	}
	log := h.logs.String()
	if !strings.Contains(log, "GGML_ASSERT(buffer) failed") || !strings.Contains(log, "exit status 134") {
		t.Errorf("the crash's log lacks the worker's last words or exit status:\n%s", log)
	}
	res, err := h.Recognize(context.Background(), speech(16000), 16000, engine.Options{})
	if err != nil || res.Text != batchText {
		t.Errorf("Recognize on the CPU = %q, %v", res.Text, err)
	}
	// Later loads go straight to the CPU, without another notice.
	h.load(t, false)
	if p := h.worker(); !p.cpu {
		t.Error("a later load went back to the GPU")
	}
	wantCodes(t, h.notices, notice.CodeEngineCPUFallback)
}

func TestLoadReplacesTheWorker(t *testing.T) {
	h := newHarness(t, harnessOptions{script: "stream"})
	h.load(t, false)
	first := h.worker()
	s, err := h.StartStream(context.Background(), engine.Options{})
	if err != nil {
		t.Fatalf("StartStream: %v", err)
	}
	h.load(t, true)
	waitDead(t, first)
	if h.worker() == first {
		t.Error("the second model loaded in the first worker")
	}
	if err := s.Push(speech(160), 16000); !errors.Is(err, engine.ErrInvalidArgument) {
		t.Errorf("Push on a stream of the old model = %v, want ErrInvalidArgument", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
	wantCodes(t, h.notices)
}

// Under the idle unloader the worker ends when the model goes idle and a
// new one starts with the next call.
func TestIdleUnloadEndsTheWorker(t *testing.T) {
	h := newHarness(t, harnessOptions{script: "batch"})
	e := autounload.New(h.Supervisor, 200*time.Millisecond, nil)
	if _, err := e.Load(context.Background(), engine.LoadSpec{ID: "fake", Path: "fake.gguf"}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := h.worker()
	waitDead(t, p)
	if e.Resident() {
		t.Error("the model is still resident")
	}
	res, err := e.Recognize(context.Background(), speech(16000), 16000, engine.Options{})
	if err != nil || res.Text != batchText {
		t.Fatalf("Recognize after the idle unload = %q, %v", res.Text, err)
	}
	if q := h.worker(); q == nil || q == p {
		t.Error("no new worker after the reload")
	}
	wantCodes(t, h.notices)
}

func TestStrayStdoutDoesNotBreakTheProtocol(t *testing.T) {
	h := newHarness(t, harnessOptions{script: "noisy"})
	h.load(t, false)
	res, err := h.Recognize(context.Background(), speech(16000), 16000, engine.Options{})
	if err != nil || res.Text != batchText {
		t.Fatalf("Recognize = %q, %v", res.Text, err)
	}
	h.logs.waitLog(t, "this line must not reach the supervisor")
}

func TestWorkerLogIsRelogged(t *testing.T) {
	h := newHarness(t, harnessOptions{script: "batch"})
	h.load(t, false)
	p := h.worker()
	h.logs.waitLog(t, `msg="engine worker listening" worker=`+strconv.Itoa(p.pid))
}
