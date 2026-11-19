//go:build unix

package hook

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// script writes a shell script with body and returns its path.
func script(t *testing.T, body string) string {
	t.Helper()
	return writeHook(t, t.TempDir(), body)
}

func TestScriptRewrites(t *testing.T) {
	big := strings.Repeat("word ", 60_000) // 300 kB: more than a pipe holds
	for _, tt := range []struct {
		name, body, in string
		info           Info
		want           string
	}{
		{name: "replaces the text", body: "tr a-z A-Z", in: "hello world", want: "HELLO WORLD"},
		{name: "drops one trailing newline", body: `echo "<$(cat)>"`, in: "hello", want: "<hello>"},
		{name: "drops only one newline", body: `printf 'a\n\n'`, want: "a\n"},
		{name: "drops a CRLF", body: `printf 'a\r\n'`, want: "a"},
		{name: "empty output pastes nothing", body: "cat >/dev/null", in: "hello", want: ""},
		{name: "may ignore its input", body: "echo ignored", in: big, want: "ignored"},
		{name: "reads all its input", body: "wc -c | tr -d ' '", in: big, want: strconv.Itoa(len(big))},
		{name: "standard error is not output", body: "echo noise >&2; printf ok", want: "ok"},
		{name: "invalid UTF-8 is replaced", body: `printf 'a\377b'`, want: "a�b"},
		{
			name: "gets the info", body: `printf '%s|%s' "$GOSCRIBER_MODEL" "$GOSCRIBER_LANGUAGE"`,
			info: Info{Model: "parakeet-tdt-0.6b-v2", Language: "pt-BR"}, want: "parakeet-tdt-0.6b-v2|pt-BR",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Script{Path: script(t, tt.body)}.Rewrite(context.Background(), tt.in, tt.info)
			if err != nil || got != tt.want {
				t.Errorf("Rewrite = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestScriptInheritsTheEnvironment(t *testing.T) {
	t.Setenv("HOOK_TEST_VALUE", "inherited")
	t.Setenv(EnvLanguage, "stale")
	p := script(t, `printf '%s|%s' "$HOOK_TEST_VALUE" "$GOSCRIBER_LANGUAGE"`)
	got, err := Script{Path: p}.Rewrite(context.Background(), "", Info{})
	if err != nil || got != "inherited|" {
		t.Errorf("Rewrite = %q, %v; want the inherited variable, and the language this transcript has (none)", got, err)
	}
	got, err = Script{Path: p, Env: []string{"HOOK_TEST_OTHER=x"}}.Rewrite(context.Background(), "", Info{Language: "fr-FR"})
	if err != nil || got != "|fr-FR" {
		t.Errorf("Rewrite with Env = %q, %v; want only the given environment and the info", got, err)
	}
}

func TestScriptRunsAFilterWithArguments(t *testing.T) {
	for _, path := range []string{"/bin/sh", "sh"} {
		s := Script{Path: path, Args: []string{"-c", "tr a-z A-Z"}}
		if got, err := s.Rewrite(context.Background(), "filter", Info{}); err != nil || got != "FILTER" {
			t.Errorf("%s: Rewrite = %q, %v", path, got, err)
		}
	}
}

func TestScriptFailures(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("#!/bin/sh\ncat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	noShebang := filepath.Join(dir, "no-shebang")
	if err := os.WriteFile(noShebang, []byte("cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name  string
		path  string
		check func(t *testing.T, err *Error)
	}{
		{"exits with a status", script(t, "echo first >&2; echo 'it broke' >&2; exit 3"), func(t *testing.T, err *Error) {
			exit, ok := errors.AsType[*exec.ExitError](err)
			if !ok || exit.ExitCode() != 3 {
				t.Errorf("err %v, want exit status 3", err)
			}
			if err.Stderr != "first\nit broke" || err.Reason() != "exit status 3: it broke" {
				t.Errorf("stderr %q, reason %q", err.Stderr, err.Reason())
			}
		}},
		{"prints too much", script(t, "dd if=/dev/zero bs=1024 count=1100 2>/dev/null"), func(t *testing.T, err *Error) {
			if !errors.Is(err, ErrOutputTooLarge) {
				t.Errorf("err %v, want ErrOutputTooLarge", err)
			}
		}},
		{"is not executable", plain, func(t *testing.T, err *Error) {
			if !errors.Is(err, os.ErrPermission) {
				t.Errorf("err %v, want a permission error", err)
			}
		}},
		{"has no #! line", noShebang, func(t *testing.T, err *Error) {
			if !errors.Is(err, syscall.ENOEXEC) {
				t.Errorf("err %v, want exec format error", err)
			}
		}},
		{"does not exist", filepath.Join(dir, "missing"), func(t *testing.T, err *Error) {
			if !errors.Is(err, os.ErrNotExist) {
				t.Errorf("err %v, want fs.ErrNotExist", err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Script{Path: tt.path}.Rewrite(context.Background(), "original", Info{})
			herr, ok := errors.AsType[*Error](err)
			if !ok || got != "" {
				t.Fatalf("Rewrite = %q, %v; want an *Error", got, err)
			}
			if herr.Path != tt.path || !strings.Contains(herr.Error(), tt.path) {
				t.Errorf("error %q names %q", herr.Error(), herr.Path)
			}
			tt.check(t, herr)
		})
	}
}

func TestScriptTimeout(t *testing.T) {
	start := time.Now()
	_, err := Script{Path: script(t, "sleep 30"), Timeout: 200 * time.Millisecond}.Rewrite(context.Background(), "x", Info{})
	if !errors.Is(err, ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Rewrite = %v, want ErrTimeout alone", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Rewrite took %v after a 200 ms timeout", took)
	}
}

func TestScriptCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, err := Script{Path: script(t, "sleep 30")}.Rewrite(ctx, "x", Info{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Rewrite = %v, want context.Canceled", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Rewrite took %v after the cancel", took)
	}
}

// TestScriptStopGivesTheHookTimeToCleanUp checks that a hook that is stopped
// is asked to first, so it can end what it started elsewhere. A timeout
// stops a hook the same way as the cancel here, which waits for the hook to
// be ready, however slowly the machine starts it.
func TestScriptStopGivesTheHookTimeToCleanUp(t *testing.T) {
	dir := t.TempDir()
	marker, ready := filepath.Join(dir, "cleaned"), filepath.Join(dir, "ready")
	p := script(t, "trap 'echo yes > "+marker+"; exit 1' TERM\necho ready > "+ready+"\nsleep 30 &\nwait")
	err := stopWhenReady(t, Script{Path: p}, ready)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Rewrite = %v, want context.Canceled", err)
	}
	if b, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(b)) != "yes" {
		t.Errorf("the hook did not clean up on SIGTERM: %q, %v", b, err)
	}
}

// TestScriptStopKillsWhatTheHookStarted checks that a hook's children go
// with it, even those that ignore SIGTERM and hold its output open.
func TestScriptStopKillsWhatTheHookStarted(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	p := script(t, "trap '' TERM\nsleep 30 &\necho $! > "+pidFile+"\nwait")
	start := time.Now()
	if err := stopWhenReady(t, Script{Path: p}, pidFile); !errors.Is(err, context.Canceled) {
		t.Errorf("Rewrite = %v, want context.Canceled", err)
	}
	if took := time.Since(start); took > grace+5*time.Second {
		t.Errorf("Rewrite took %v", took)
	}
	waitGone(t, readPID(t, pidFile))
}

// stopWhenReady runs s, cancels it once the script has written to the file
// ready, and returns Rewrite's error.
func stopWhenReady(t *testing.T, s Script, ready string) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.Rewrite(ctx, "x", Info{})
		done <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(ready); err == nil && strings.TrimSpace(string(b)) != "" {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the hook ended before it was ready: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the hook did not get ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	return <-done
}

// readPID reads the process ID a script wrote to file.
func readPID(t *testing.T, file string) int {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// TestScriptLeavesAFinishedHooksChildren checks that a hook that exits on its
// own keeps what it started in the background, and its output counts even
// though that child holds the output open.
func TestScriptLeavesAFinishedHooksChildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	p := script(t, "sleep 5 &\necho $! > "+pidFile+"\necho done")
	start := time.Now()
	got, err := Script{Path: p}.Rewrite(context.Background(), "x", Info{})
	if err != nil || got != "done" {
		t.Errorf("Rewrite = %q, %v; want the hook's output", got, err)
	}
	if took := time.Since(start); took > grace+5*time.Second {
		t.Errorf("Rewrite took %v", took)
	}
	pid := readPID(t, pidFile)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("the hook's background child is gone: %v", err)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// TestMainIsAHook runs the test binary as a hook program built with Main
// (see TestMain), through Script: both ends of the contract together.
func TestMainIsAHook(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	upper := Script{Path: exe, Env: append(os.Environ(), mainEnv+"=upper")}
	got, err := upper.Rewrite(context.Background(), "olá mundo", Info{Language: "pt-BR"})
	if err != nil || got != "OLÁ MUNDO (pt-BR)" {
		t.Errorf("Rewrite = %q, %v", got, err)
	}

	failing := Script{Path: exe, Env: append(os.Environ(), mainEnv+"=fail")}
	_, err = failing.Rewrite(context.Background(), "x", Info{})
	exit, ok := errors.AsType[*exec.ExitError](err)
	if !ok || exit.ExitCode() != 1 || !strings.HasSuffix(err.Error(), ": the workflow is down") {
		t.Errorf("Rewrite = %v, want exit status 1 with the hook's error", err)
	}
}

func TestFind(t *testing.T) {
	dir := t.TempDir()
	p := writeHook(t, dir, "cat")
	s, err := Find(dir)
	if err != nil || s.Path != p || s.Timeout != 0 {
		t.Errorf("Find = %+v, %v; want %s with the default timeout", s, err, p)
	}

	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Find(dir); !errors.Is(err, ErrNotExecutable) {
		t.Errorf("Find of a file without execute permission: %v, want ErrNotExecutable", err)
	}

	linked := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(Path(linked)), 0o700); err != nil {
		t.Fatal(err)
	}
	target := script(t, "tr a-z A-Z")
	if err := os.Symlink(target, Path(linked)); err != nil {
		t.Fatal(err)
	}
	s, err = Find(linked)
	if err != nil || s.Path != Path(linked) {
		t.Fatalf("Find of a symbolic link = %+v, %v", s, err)
	}
	if got, err := s.Rewrite(context.Background(), "linked", Info{}); err != nil || got != "LINKED" {
		t.Errorf("Rewrite through the link = %q, %v", got, err)
	}
}

// waitGone waits for process pid to be gone.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !gone(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("process %d still runs", pid)
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
