//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/opragmatico/goscriber/internal/instance"
)

// runningApp stands in for a Goscriber that is running in home: it holds
// the lock and records the commands it is sent.
type runningApp struct {
	mu   sync.Mutex
	cmds []instance.Command
	err  error
}

func startRunningApp(t *testing.T, home string) *runningApp {
	t.Helper()
	inst, err := instance.Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	app := &runningApp{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = inst.Serve(ctx, app.handle)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		inst.Close()
	})
	return app
}

func (a *runningApp) handle(c instance.Command) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cmds = append(a.cmds, c)
	return a.err
}

func (a *runningApp) got() []instance.Command {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]instance.Command(nil), a.cmds...)
}

// The remote-control flags reach the running app and exit quietly.
func TestRemoteFlagsReachTheRunningApp(t *testing.T) {
	app := startRunningApp(t, isolate(t))
	for _, tt := range []struct {
		flag string
		want instance.Command
	}{
		{"--toggle-transcription", instance.Toggle},
		{"--cancel", instance.Cancel},
	} {
		code, stdout, stderr := run(t, "", tt.flag)
		if code != 0 || stdout != "" || stderr != "" {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want a quiet success", tt.flag, code, stdout, stderr)
		}
		if got := app.got(); len(got) == 0 || got[len(got)-1] != tt.want {
			t.Errorf("%s: the app got %v", tt.flag, got)
		}
	}
}

// With no app to control, the command line says so and fails, without
// starting one.
func TestRemoteFlagWithNoApp(t *testing.T) {
	isolate(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"--toggle-transcription"}, Env{Stdout: &out, Stderr: &errOut, RunApp: func(AppOptions) error {
		t.Error("a remote-control flag started the app")
		return nil
	}})
	if code != 1 || !strings.Contains(errOut.String(), "not running") {
		t.Errorf("exit %d, stderr %q", code, errOut.String())
	}
}

func TestRemoteFlagRefusedByTheApp(t *testing.T) {
	app := startRunningApp(t, isolate(t))
	app.err = errors.New("the app is quitting")
	code, _, stderr := run(t, "", "--cancel")
	if code != 1 || !strings.Contains(stderr, "the app is quitting") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

func TestRemoteFlagsUsage(t *testing.T) {
	isolate(t)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"--toggle-transcription", "--cancel"}, "cannot be combined"},
		{[]string{"--toggle-transcription", "--no-tray"}, "takes no other flags (--no-tray)"},
		{[]string{"--debug", "--cancel"}, "takes no other flags (--debug)"},
	} {
		code, stdout, stderr := run(t, "", tt.args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, tt.want) || !strings.Contains(stderr, "Usage:") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want a usage error with %q", tt.args, code, stdout, stderr, tt.want)
		}
	}
	code, stdout, _ := run(t, "", "--help")
	if code != 0 || !strings.Contains(stdout, "--toggle-transcription") || !strings.Contains(stdout, "--cancel") || !strings.Contains(stdout, "SIGUSR2") {
		t.Errorf("the usage does not describe remote control: %q", stdout)
	}
}

// The first launch holds the lock while the app runs and lets go after; a
// second launch shows the running app's window instead of starting another.
func TestSingleInstance(t *testing.T) {
	home := isolate(t)
	var sawLock bool
	code := Run(nil, Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, RunApp: func(o AppOptions) error {
		if o.Instance == nil {
			t.Fatal("the app was started without the single-instance lock")
		}
		_, err := instance.Acquire(home)
		sawLock = errors.Is(err, instance.ErrRunning)
		return nil
	}})
	if code != 0 || !sawLock {
		t.Fatalf("exit %d; lock held while the app ran: %v", code, sawLock)
	}
	if inst, err := instance.Acquire(home); err != nil {
		t.Fatalf("the lock was not released after the app returned: %v", err)
	} else {
		inst.Close()
	}

	app := startRunningApp(t, home)
	var out, errOut bytes.Buffer
	code = Run([]string{"--start-hidden"}, Env{Stdout: &out, Stderr: &errOut, RunApp: func(AppOptions) error {
		t.Error("a second Goscriber started")
		return nil
	}})
	if code != 0 || !strings.Contains(out.String(), "already running") || errOut.String() != "" {
		t.Errorf("second launch: exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
	if got := app.got(); len(got) != 1 || got[0] != instance.Show {
		t.Errorf("the running app got %v, want a request to show its window", got)
	}
}

// The login item starting Goscriber while it already runs changes nothing:
// no second app, no window brought to the front, no output.
func TestLaunchAtLoginLeavesARunningAppBe(t *testing.T) {
	app := startRunningApp(t, isolate(t))
	var out, errOut bytes.Buffer
	code := Run([]string{"--at-login"}, Env{Stdout: &out, Stderr: &errOut, RunApp: func(AppOptions) error {
		t.Error("a second Goscriber started")
		return nil
	}})
	if code != 0 || out.String() != "" || errOut.String() != "" {
		t.Errorf("exit %d, stdout %q, stderr %q; want a quiet 0", code, out.String(), errOut.String())
	}
	if got := app.got(); len(got) != 0 {
		t.Errorf("the running app got %v, want nothing", got)
	}
}
