package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func run(t *testing.T, version string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = Run(args, Env{Stdout: &out, Stderr: &errOut, Version: version})
	return code, out.String(), errOut.String()
}

func TestHelp(t *testing.T) {
	for _, flag := range []string{"-h", "--help", "-help"} {
		code, stdout, stderr := run(t, "", flag)
		if code != 0 {
			t.Errorf("%s: exit code %d, want 0", flag, code)
		}
		if !strings.Contains(stdout, "Usage:") || !strings.Contains(stdout, "--version") {
			t.Errorf("%s: usage not printed to stdout: %q", flag, stdout)
		}
		if stderr != "" {
			t.Errorf("%s: unexpected stderr %q", flag, stderr)
		}
	}
}

func TestVersion(t *testing.T) {
	code, stdout, _ := run(t, "v1.2.3", "--version")
	if code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if !strings.HasPrefix(stdout, "goscriber v1.2.3 (go") {
		t.Errorf("unexpected version line %q", stdout)
	}
}

func TestVersionFallsBackToBuildInfo(t *testing.T) {
	if got := ResolveVersion(""); got == "" {
		t.Error("ResolveVersion returned an empty string")
	}
	if got := ResolveVersion("v9"); got != "v9" {
		t.Errorf("ResolveVersion(%q) = %q", "v9", got)
	}
}

func TestBadUsage(t *testing.T) {
	for _, args := range [][]string{{"--nope"}, {"extra"}} {
		code, stdout, stderr := run(t, "", args...)
		if code != 2 {
			t.Errorf("%v: exit code %d, want 2", args, code)
		}
		if stdout != "" || !strings.Contains(stderr, "Usage:") {
			t.Errorf("%v: want usage on stderr only, got stdout=%q stderr=%q", args, stdout, stderr)
		}
	}
}

func TestNoAppInBuild(t *testing.T) {
	code, _, stderr := run(t, "")
	if code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if !strings.Contains(stderr, "not available") {
		t.Errorf("unexpected stderr %q", stderr)
	}
}

func TestRunsAppWithFlags(t *testing.T) {
	for _, tt := range []struct {
		args  []string
		debug bool
	}{
		{nil, false},
		{[]string{"--debug"}, true},
	} {
		var got *AppOptions
		var out, errOut bytes.Buffer
		code := Run(tt.args, Env{Stdout: &out, Stderr: &errOut, Version: "v1.0.0", RunApp: func(o AppOptions) error {
			got = &o
			return nil
		}})
		if code != 0 || got == nil {
			t.Fatalf("%v: exit %d, app started: %v", tt.args, code, got != nil)
		}
		if got.Debug != tt.debug || got.Version != "v1.0.0" {
			t.Errorf("%v: app options %+v", tt.args, *got)
		}
	}
}

func TestAppErrorExitsNonZero(t *testing.T) {
	var errOut bytes.Buffer
	code := Run(nil, Env{Stdout: &bytes.Buffer{}, Stderr: &errOut, RunApp: func(AppOptions) error {
		return errors.New("no settings for you")
	}})
	if code != 1 || !strings.Contains(errOut.String(), "no settings for you") {
		t.Errorf("exit %d, stderr %q", code, errOut.String())
	}
}
