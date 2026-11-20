package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/engine"
	"github.com/opragmatico/goscriber/engine/fake"
	"github.com/opragmatico/goscriber/internal/notice"
)

// The tests run their own binary as the worker: with envScript set,
// TestMain hosts a scripted fake engine instead of running the tests, so
// crashes, hangs and stray output need no native engine.
const (
	envScript = "GOSCRIBER_TEST_WORKER"       // the script to host
	envDelay  = "GOSCRIBER_TEST_WORKER_DELAY" // how long each Recognize takes
)

// The transcripts the scripts produce.
const (
	batchText  = "ask not what your country can do for you"
	streamText = "and so my fellow americans"
	wholeText  = "decoded whole"
)

func TestMain(m *testing.M) {
	if name := os.Getenv(envScript); name != "" {
		os.Exit(runScript(name))
	}
	os.Exit(m.Run())
}

// runScript is the worker process of the tests.
func runScript(name string) int {
	delay, _ := time.ParseDuration(os.Getenv(envDelay))
	var eng engine.Engine
	switch name {
	case "batch":
		eng = fake.New(fake.Script{Replies: []fake.Reply{{Result: engine.Result{Text: batchText, Confidence: 1}, Delay: delay}}})
	case "stream":
		eng = fake.New(fake.Script{
			Model:   engine.ModelInfo{Streaming: true, Languages: []string{"auto", "en-US"}},
			Replies: []fake.Reply{{Result: engine.Result{Text: wholeText, Confidence: 1}, Delay: delay}},
			Streams: []fake.StreamScript{fake.Dictation(streamText, 100*time.Millisecond)},
		})
	case "load-error":
		eng = fake.New(fake.Script{LoadErr: engine.ErrModelNotFound})
	case "gpu-crash":
		// Loading on the GPU aborts, as a broken driver would.
		eng = gpuCrash{fake.New(fake.Script{Replies: []fake.Reply{fake.Transcript(batchText)}})}
	case "noisy":
		eng = noisy{fake.New(fake.Script{Replies: []fake.Reply{fake.Transcript(batchText)}})}
	default:
		fmt.Fprintln(os.Stderr, "unknown test worker script", name)
		return 2
	}
	if err := Run(eng); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// gpuCrash is an engine whose loads on the GPU abort the process.
type gpuCrash struct{ engine.Engine }

func (g gpuCrash) Load(ctx context.Context, spec engine.LoadSpec) (engine.ModelInfo, error) {
	if !spec.CPU {
		fmt.Fprintln(os.Stderr, "ggml-metal.m:1234: GGML_ASSERT(buffer) failed")
		os.Exit(134)
	}
	return g.Engine.Load(ctx, spec)
}

// noisy is an engine that prints to stdout while it loads, as native code
// might.
type noisy struct{ engine.Engine }

func (n noisy) Load(ctx context.Context, spec engine.LoadSpec) (engine.ModelInfo, error) {
	fmt.Println("this line must not reach the supervisor")
	return n.Engine.Load(ctx, spec)
}

// testCommand starts this test binary as a worker hosting script.
func testCommand(script string, delay time.Duration) func() (*exec.Cmd, error) {
	return func() (*exec.Cmd, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), envScript+"="+script, envDelay+"="+delay.String(),
			// A -race binary otherwise sleeps a second as it exits.
			"GORACE=atexit_sleep_ms=0 "+os.Getenv("GORACE"))
		return cmd, nil
	}
}

// harness is a Supervisor over a test worker, with what it published and
// logged.
type harness struct {
	*Supervisor
	notices *notices
	logs    *logBuffer
}

type harnessOptions struct {
	script   string
	delay    time.Duration
	timeouts Timeouts
}

func newHarness(t *testing.T, o harnessOptions) *harness {
	t.Helper()
	h := &harness{notices: &notices{}, logs: &logBuffer{}}
	h.Supervisor = New(Options{
		Command:  testCommand(o.script, o.delay),
		Notices:  h.notices,
		Log:      slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Timeouts: o.timeouts,
	})
	t.Cleanup(func() {
		if err := h.Unload(); err != nil {
			t.Errorf("Unload: %v", err)
		}
	})
	return h
}

// worker returns the worker holding the model, or nil.
func (h *harness) worker() *proc {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.w
}

// load loads a model with spec's CPU choice.
func (h *harness) load(t *testing.T, cpu bool) engine.ModelInfo {
	t.Helper()
	info, err := h.Load(context.Background(), engine.LoadSpec{ID: "fake", Path: "fake.gguf", CPU: cpu})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return info
}

// kill ends the worker as a crash would, and waits until it is gone.
func (h *harness) kill(t *testing.T) *proc {
	t.Helper()
	p := h.worker()
	if p == nil {
		t.Fatal("no worker to kill")
	}
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	waitDead(t, p)
	return p
}

// waitBusy waits until a call on p is waiting for an answer to o.
func waitBusy(t *testing.T, p *proc, o op) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		p.mu.Lock()
		busy := false
		for _, c := range p.pending {
			busy = busy || c.op == o
		}
		p.mu.Unlock()
		if busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker %d never got a %s request", p.pid, o)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitDead(t *testing.T, p *proc) {
	t.Helper()
	select {
	case <-p.dead:
	case <-time.After(10 * time.Second):
		t.Fatalf("worker %d still running", p.pid)
	}
}

// notices records what was published.
type notices struct {
	mu  sync.Mutex
	got []notice.Notice
}

func (n *notices) Publish(x notice.Notice) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.got = append(n.got, x)
	return true
}

func (n *notices) Resolve(notice.Code) bool { return false }

func (n *notices) codes() []notice.Code {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []notice.Code
	for _, x := range n.got {
		out = append(out, x.Code)
	}
	return out
}

func (n *notices) messages() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, x := range n.got {
		out = append(out, x.Message)
	}
	return out
}

func wantCodes(t *testing.T, n *notices, want ...notice.Code) {
	t.Helper()
	if got := n.codes(); !slices.Equal(got, want) {
		t.Errorf("notices = %v, want %v (%q)", got, want, n.messages())
	}
}

// logBuffer collects log output from any goroutine.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitLog waits until the log holds want.
func (b *logBuffer) waitLog(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(b.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("log never had %q; log:\n%s", want, b.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func wantKind(t *testing.T, err error, kinds ...error) {
	t.Helper()
	if _, ok := errors.AsType[*engine.Error](err); !ok {
		t.Fatalf("err = %v (%T), want an *engine.Error", err, err)
	}
	for _, k := range kinds {
		if !errors.Is(err, k) {
			t.Fatalf("err = %v, want it to match %v", err, k)
		}
	}
}

// speech is n samples of a loud tone at 16 kHz, enough for the speech gate.
func speech(n int) []float32 {
	s := make([]float32, n)
	for i := range s {
		if i%40 < 20 {
			s[i] = 0.3
		} else {
			s[i] = -0.3
		}
	}
	return s
}
