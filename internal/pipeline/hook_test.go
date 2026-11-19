//go:build unix

package pipeline

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/hook"
	"github.com/opragmatico/goscriber/internal/engine/fake"
	"github.com/opragmatico/goscriber/internal/notice"
)

// testHookTimeout stands in for the hook's 30 s in the tests of a hook that
// runs too long; the others keep the 30 s, so a slow machine does not time
// them out.
const testHookTimeout = 300 * time.Millisecond

// timeoutFor is the timeout of a test's hook: testHookTimeout for one that
// sleeps through it, else the default.
func timeoutFor(body string) time.Duration {
	if strings.Contains(body, "sleep") {
		return testHookTimeout
	}
	return 0
}

// writeHook installs a shell script with body as the transcription hook in
// configDir.
func writeHook(t *testing.T, configDir, body string, mode os.FileMode) {
	t.Helper()
	p := hook.Path(configDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), mode); err != nil {
		t.Fatal(err)
	}
}

func TestHookStage(t *testing.T) {
	info := SessionInfo{Session: 7, Model: "parakeet-tdt-0.6b-v2", TextLanguage: "en"}
	for _, tt := range []struct {
		name string
		body string // the hook's script; "" for no hook
		mode os.FileMode
		in   string
		want string
		// failed: Process fails, and the notice says why.
		failed string
	}{
		{name: "no hook", in: "hello", want: "hello"},
		{
			name: "rewrites with the session's model and language",
			body: `printf '%s [%s %s]' "$(cat)" "$GOSCRIBER_MODEL" "$GOSCRIBER_LANGUAGE"`,
			in:   "hello", want: "hello [parakeet-tdt-0.6b-v2 en]",
		},
		{name: "a blank transcript does not run it", body: "exit 1", in: " \n", want: " \n"},
		{name: "empty output pastes nothing", body: "cat >/dev/null", in: "hello", want: ""},
		{name: "fails", body: "echo 'no network' >&2; exit 1", in: "hello", failed: "exit status 1: no network"},
		{name: "times out", body: "sleep 30", in: "hello", failed: "ran longer than 30 seconds"},
		{name: "not executable", body: "cat", mode: 0o644, in: "hello", failed: "is not executable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.body != "" {
				writeHook(t, dir, tt.body, cmp.Or(tt.mode, 0o755))
			}
			notices := &fakeNotices{}
			stage := HookStage{ConfigDir: dir, Notices: notices, timeout: timeoutFor(tt.body)}
			got, err := stage.Process(context.Background(), tt.in, info)
			if tt.failed == "" {
				if err != nil || got != tt.want || len(notices.codes()) != 0 {
					t.Errorf("Process = %q, %v, notices %v; want %q", got, err, notices.codes(), tt.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("Process = %q, want an error", got)
			}
			notices.mu.Lock()
			defer notices.mu.Unlock()
			if len(notices.published) != 1 || notices.published[0].Code != notice.CodeHookFailed ||
				!strings.Contains(notices.published[0].Message, tt.failed) {
				t.Errorf("notices %+v, want hook_failed saying %q", notices.published, tt.failed)
			}
		})
	}
}

func TestHookStageCancelledRaisesNoNotice(t *testing.T) {
	dir := t.TempDir()
	writeHook(t, dir, "sleep 30", 0o755)
	notices := &fakeNotices{}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := (HookStage{ConfigDir: dir, Notices: notices}).Process(ctx, "hello", SessionInfo{}); err == nil {
		t.Error("Process succeeded after the cancel")
	}
	if codes := notices.codes(); len(codes) != 0 {
		t.Errorf("notices %v after a cancel", codes)
	}
}

// TestHookInDictation runs the hook in dictation: what it prints is pasted
// and saved; when it fails, the transcript it was given is.
func TestHookInDictation(t *testing.T) {
	for _, tt := range []struct {
		name      string
		body      string
		delivered string // "" for nothing
		failed    bool
	}{
		{"rewrites", "tr a-z A-Z", "HELLO THERE", false},
		{"fails", "exit 2", "hello there", true},
		{"times out", "sleep 30", "hello there", true},
		{"prints nothing", "cat >/dev/null", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeHook(t, dir, tt.body, 0o755)
			notices := &fakeNotices{}
			history := &historyLog{}
			r := newRig(t, rigOptions{
				script: fake.Script{Replies: []fake.Reply{fake.Transcript("hello there")}},
				stages: Stages{
					Cleanup: CleanupStage{},
					Hook:    HookStage{ConfigDir: dir, Notices: notices, timeout: timeoutFor(tt.body)},
					History: history,
				},
			})
			r.c.Start()
			r.events.waitState(t, StateRecording, 1)
			r.c.Stop()
			r.events.waitState(t, StateIdle, 1)

			var want []string
			if tt.delivered != "" {
				want = []string{tt.delivered}
			}
			if got := r.out.delivered(); !slices.Equal(got, want) {
				t.Errorf("delivered %q, want %q", got, want)
			}
			if got, failed := notices.codes(), slices.Equal(notices.codes(), []notice.Code{notice.CodeHookFailed}); failed != tt.failed {
				t.Errorf("notices %v", got)
			}
			e := history.wait(t)
			if e.RawText != "hello there" || e.Text != tt.delivered || e.Delivered != (tt.delivered != "") {
				t.Errorf("history entry: raw %q, text %q, delivered %v", e.RawText, e.Text, e.Delivered)
			}
		})
	}
}

// TestCancelStopsTheHook cancels a dictation while its hook runs: the hook
// is stopped, nothing is pasted and nobody is told.
func TestCancelStopsTheHook(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "pid")
	writeHook(t, dir, "echo $$ > "+pidFile+"\nexec sleep 30", 0o755)
	notices := &fakeNotices{}
	r := newRig(t, rigOptions{
		script: fake.Script{Replies: []fake.Reply{fake.Transcript("hello there")}},
		stages: Stages{Hook: HookStage{ConfigDir: dir, Notices: notices}},
	})
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StatePostProcessing, 1)
	pid := waitPID(t, pidFile)
	r.c.Cancel()
	r.events.waitState(t, StateIdle, 1)

	waitGone(t, pid)
	if got := r.out.delivered(); len(got) != 0 {
		t.Errorf("delivered %q after the cancel", got)
	}
	if codes := r.notices.codes(); len(codes) != 0 {
		t.Errorf("coordinator notices %v", codes)
	}
	if codes := notices.codes(); len(codes) != 0 {
		t.Errorf("hook notices %v", codes)
	}
}

// historyLog is a HistorySink that keeps what it is given.
type historyLog struct {
	mu      sync.Mutex
	entries []HistoryEntry
}

func (h *historyLog) Save(_ context.Context, e HistoryEntry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, e)
	return nil
}

// wait returns the first entry saved, waiting for it: history saves after
// the paste.
func (h *historyLog) wait(t *testing.T) HistoryEntry {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.mu.Lock()
		if len(h.entries) > 0 {
			defer h.mu.Unlock()
			return h.entries[0]
		}
		h.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("nothing was saved to history")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitPID waits for a hook to write its process ID to file, and returns it.
func waitPID(t *testing.T, file string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if b, err := os.ReadFile(file); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the hook did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitGone waits for process pid to be gone.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !gone(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the hook (process %d) still runs", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// gone reports whether process pid has ended: it is no more, or it is a
// zombie nobody reaps, as under a container's init that reaps no orphans.
func gone(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(b) // pid (command) state ...
	i := strings.LastIndexByte(s, ')')
	return i >= 0 && i+2 < len(s) && s[i+2] == 'Z'
}
