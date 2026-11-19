package hook

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opragmatico/goscriber/internal/config"
)

// TestMain also runs the test binary as a hook program, built with Main,
// when TestMainIsAHook runs it with mainEnv set.
func TestMain(m *testing.M) {
	switch os.Getenv(mainEnv) {
	case "":
		os.Exit(m.Run())
	case "upper":
		Main(Func(func(_ context.Context, text string, info Info) (string, error) {
			return strings.ToUpper(text) + " (" + info.Language + ")\n", nil
		}))
		os.Exit(0)
	default:
		Main(Func(func(context.Context, string, Info) (string, error) {
			return "", errors.New("the workflow is down")
		}))
		os.Exit(0)
	}
}

// mainEnv tells the test binary to act as a hook program.
const mainEnv = "GOSCRIBER_HOOK_TEST_MAIN"

func TestPath(t *testing.T) {
	want := filepath.Join("cfg", "hooks", "transcription")
	if got := Path("cfg"); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestFindNoHook(t *testing.T) {
	dir := t.TempDir()
	if _, err := Find(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Find with no hooks directory: %v, want fs.ErrNotExist", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Find(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Find with an empty hooks directory: %v, want fs.ErrNotExist", err)
	}
}

func TestFindRefusesADirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(Path(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Find(dir)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("Find = %v, want an error for a hook that cannot run", err)
	}
}

func TestDefaultFollowsGoscriberHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.HomeEnv, dir)
	if _, err := Default(); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Default with no hook: %v, want fs.ErrNotExist", err)
	}
	writeHook(t, dir, "cat")
	s, err := Default()
	if err != nil || s.Path != Path(dir) {
		t.Errorf("Default = %+v, %v; want the hook in %s", s, err, dir)
	}
}

func TestFunc(t *testing.T) {
	var h Hook = Func(func(_ context.Context, text string, info Info) (string, error) {
		return text + "/" + info.Model, nil
	})
	got, err := h.Rewrite(context.Background(), "text", Info{Model: "m"})
	if err != nil || got != "text/m" {
		t.Errorf("Rewrite = %q, %v", got, err)
	}
}

func TestRun(t *testing.T) {
	t.Setenv(EnvModel, "nemotron-3.5-asr-streaming-0.6b")
	t.Setenv(EnvLanguage, "de-DE")
	var got Info
	var out bytes.Buffer
	err := Run(context.Background(), Func(func(_ context.Context, text string, info Info) (string, error) {
		got = info
		return strings.ToUpper(text), nil
	}), strings.NewReader("hallo welt\n"), &out)
	if err != nil || out.String() != "HALLO WELT\n" {
		t.Errorf("Run wrote %q, %v; want the text as the hook returned it", out.String(), err)
	}
	if want := (Info{Model: "nemotron-3.5-asr-streaming-0.6b", Language: "de-DE"}); got != want {
		t.Errorf("hook got %+v, want %+v", got, want)
	}

	out.Reset()
	boom := errors.New("boom")
	err = Run(context.Background(), Func(func(context.Context, string, Info) (string, error) {
		return "partial", boom
	}), strings.NewReader("x"), &out)
	if !errors.Is(err, boom) || out.Len() != 0 {
		t.Errorf("Run = %v, wrote %q; want the hook's error and nothing written", err, out.String())
	}
}

func TestOutput(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"text", "text"},
		{"text\n", "text"},
		{"text\r\n", "text"},
		{"text\n\n", "text\n"},
		{"text\r", "text\r"},
		{"\n", ""},
		{"a\xffb", "a�b"},
	} {
		if got := output(tt.in); got != tt.want {
			t.Errorf("output(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestErrorReason(t *testing.T) {
	e := &Error{Path: "/h", Err: ErrTimeout, Stderr: "starting\nstill waiting  \n\n"}
	if got := e.Reason(); got != "timed out: still waiting" {
		t.Errorf("Reason = %q", got)
	}
	if got := e.Error(); got != "hook /h: timed out: still waiting" {
		t.Errorf("Error = %q", got)
	}
	if !errors.Is(e, ErrTimeout) {
		t.Error("the error does not match its reason")
	}
	if got := (&Error{Path: "/h", Err: ErrOutputTooLarge}).Reason(); got != ErrOutputTooLarge.Error() {
		t.Errorf("Reason without stderr = %q", got)
	}
}

func TestCappedKeepsTheStartAndNeverBlocks(t *testing.T) {
	c := &capped{max: 5}
	for _, p := range []string{"abc", "defg", "hij"} {
		if n, err := c.Write([]byte(p)); n != len(p) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", p, n, err)
		}
	}
	if c.buf.String() != "abcde" || !c.over {
		t.Errorf("kept %q, over %v", c.buf.String(), c.over)
	}
	c = &capped{max: 5}
	_, _ = c.Write([]byte("abcde"))
	if c.over {
		t.Error("exactly max bytes counted as too many")
	}
}

func TestTailKeepsTheEnd(t *testing.T) {
	tl := &tail{max: 8}
	for _, p := range []string{"one ", "two ", "three ", "four"} {
		if n, err := tl.Write([]byte(p)); n != len(p) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", p, n, err)
		}
	}
	if got := tl.String(); got != "ree four" {
		t.Errorf("tail = %q, want the last 8 bytes, trimmed", got)
	}
	_, _ = tl.Write([]byte(strings.Repeat("x", 20) + "end"))
	if got := tl.String(); got != "xxxxxend" {
		t.Errorf("tail after a long write = %q", got)
	}
}

// writeHook writes a shell script with body as the hook in configDir.
func writeHook(t *testing.T, configDir, body string) string {
	t.Helper()
	p := Path(configDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}
