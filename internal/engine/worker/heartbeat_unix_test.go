//go:build unix

package worker

import (
	"context"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/engine"
	"github.com/opragmatico/goscriber/internal/notice"
)

// A frozen worker misses its heartbeat even while idle: it is ended, and the
// next call starts a new one.
func TestHeartbeatEndsAFrozenWorker(t *testing.T) {
	h := newHarness(t, harnessOptions{
		script:   "batch",
		timeouts: Timeouts{Heartbeat: 20 * time.Millisecond, HeartbeatTimeout: 200 * time.Millisecond},
	})
	h.load(t, false)
	p := h.worker()
	if err := syscall.Kill(p.pid, syscall.SIGSTOP); err != nil {
		t.Fatalf("SIGSTOP: %v", err)
	}
	waitDead(t, p)
	// Idle, so not blamed on the GPU.
	wantCodes(t, h.notices, notice.CodeEngineCrashed)
	if msg := h.notices.messages()[0]; !strings.Contains(msg, "stopped responding while idle") {
		t.Errorf("notice = %q", msg)
	}
	res, err := h.Recognize(context.Background(), speech(16000), 16000, engine.Options{})
	if err != nil || res.Text != batchText {
		t.Fatalf("Recognize after the hang = %q, %v", res.Text, err)
	}
	if q := h.worker(); q == p || q.cpu {
		t.Error("the next call did not start a new GPU worker")
	}
}
