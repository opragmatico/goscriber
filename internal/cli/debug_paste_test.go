package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/output"
)

// fakeDesk is a clipboard and keyboard that record what happened.
type fakeDesk struct {
	clip     string
	has      bool
	changes  int
	pasted   []string
	typed    string
	returns  int
	pasteErr error
	// settings is what debug paste handed the keyboard.
	settings config.Settings
}

func (d *fakeDesk) Save() (func() error, error) {
	clip, has := d.clip, d.has
	return func() error { d.clip, d.has = clip, has; d.changes++; return nil }, nil
}

func (d *fakeDesk) SetText(text string, _ bool) error {
	d.clip, d.has = text, true
	d.changes++
	return nil
}

func (d *fakeDesk) ChangeCount() int { return d.changes }

func (d *fakeDesk) Paste() error {
	if d.pasteErr != nil {
		return d.pasteErr
	}
	d.pasted = append(d.pasted, d.clip)
	return nil
}

func (d *fakeDesk) Type(text string) error { d.typed += text; return nil }
func (d *fakeDesk) PressReturn() error     { d.returns++; return nil }

func (d *fakeDesk) env() Env {
	return Env{Output: func(s config.Settings) OutputEnv {
		d.settings = s
		return OutputEnv{
			Clipboard: d, Keyboard: d,
			Text:     func() (string, bool) { return d.clip, d.has },
			PasteKey: func() string { return "Cmd+V (key code 9)" },
		}
	}}
}

func TestDebugPaste(t *testing.T) {
	t.Setenv(config.HomeEnv, t.TempDir())
	d := &fakeDesk{clip: "user's copy", has: true}
	code, stdout, stderr := runWith(t, d.env(), "debug", "paste", "--wait", "1ms", "Ask", "not.")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if len(d.pasted) != 1 || d.pasted[0] != "Ask not." || d.clip != "user's copy" {
		t.Errorf("pasted %q, clipboard %q", d.pasted, d.clip)
	}
	for _, want := range []string{"paste with Cmd+V (key code 9) in 1ms", "Pasted 8 characters. The clipboard holds what it held before."} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout %q lacks %q", stdout, want)
		}
	}
}

func TestDebugPasteMethods(t *testing.T) {
	t.Setenv(config.HomeEnv, t.TempDir())
	for _, tc := range []struct {
		args  []string
		want  string
		check func(*fakeDesk) error
	}{
		{[]string{"--method", "direct", "--submit"}, "Typed 3 characters and pressed Return. The clipboard holds what it held before.", func(d *fakeDesk) error {
			if d.typed != "Hi." || d.returns != 1 || len(d.pasted) != 0 {
				return fmt.Errorf("typed %q, %d returns, pasted %q", d.typed, d.returns, d.pasted)
			}
			return nil
		}},
		{[]string{"--method", "none", "--space"}, "Copied 4 characters. The clipboard holds the text.", func(d *fakeDesk) error {
			if d.clip != "Hi. " || len(d.pasted) != 0 {
				return fmt.Errorf("clipboard %q, pasted %q", d.clip, d.pasted)
			}
			return nil
		}},
		{[]string{"--keep"}, "Pasted 3 characters. The clipboard holds the text.", nil},
	} {
		d := &fakeDesk{clip: "user's copy", has: true}
		args := append(append([]string{"debug", "paste", "--wait", "0"}, tc.args...), "Hi.")
		code, stdout, stderr := runWith(t, d.env(), args...)
		if code != 0 || !strings.Contains(stdout, tc.want) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", tc.args, code, stdout, stderr)
		}
		if tc.check != nil {
			if err := tc.check(d); err != nil {
				t.Errorf("%v: %v", tc.args, err)
			}
		}
	}
}

// --keys and --tool choose how Linux presses keys for this one paste.
func TestDebugPasteKeysAndTool(t *testing.T) {
	t.Setenv(config.HomeEnv, t.TempDir())
	d := &fakeDesk{}
	code, _, stderr := runWith(t, d.env(), "debug", "paste", "--wait", "0", "--keys", "ctrl_shift_v", "--tool", "ydotool", "Hi.")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if d.settings.PasteKeys != config.PasteCtrlShiftV || d.settings.TypingTool != config.TypingYdotool {
		t.Errorf("the keyboard got paste keys %q and tool %q", d.settings.PasteKeys, d.settings.TypingTool)
	}
	code, _, _ = runWith(t, d.env(), "debug", "paste", "--wait", "0", "Hi.")
	if def := config.Defaults(); code != 0 || d.settings.PasteKeys != def.PasteKeys || d.settings.TypingTool != def.TypingTool {
		t.Errorf("without flags: exit %d, paste keys %q, tool %q; want the settings'", code, d.settings.PasteKeys, d.settings.TypingTool)
	}
}

func TestDebugPasteWithoutPermission(t *testing.T) {
	t.Setenv(config.HomeEnv, t.TempDir())
	d := &fakeDesk{clip: "user's copy", has: true, pasteErr: fmt.Errorf("%w: grant it", output.ErrNoPermission)}
	code, stdout, stderr := runWith(t, d.env(), "debug", "paste", "--wait", "0", "Hi.")
	if code != 1 || !strings.Contains(stderr, "no permission") || !strings.Contains(stderr, "asks the terminal app") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "The clipboard holds what it held before.") || d.clip != "user's copy" {
		t.Errorf("stdout %q, clipboard %q", stdout, d.clip)
	}
}

func TestDebugPasteUsageErrors(t *testing.T) {
	t.Setenv(config.HomeEnv, t.TempDir())
	d := &fakeDesk{}
	for _, tc := range []struct {
		env  Env
		args []string
		want string
	}{
		{d.env(), []string{"debug", "paste"}, "no TEXT given"},
		{d.env(), []string{"debug", "paste", "--method", "shout", "x"}, `--method "shout"`},
		{d.env(), []string{"debug", "paste", "--keys", "alt_v", "x"}, `--keys "alt_v"`},
		{d.env(), []string{"debug", "paste", "--tool", "xte", "x"}, `--tool "xte"`},
		{Env{}, []string{"debug", "paste", "x"}, "this build cannot paste"},
	} {
		code, _, stderr := runWith(t, tc.env, tc.args...)
		if code == 0 || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v: exit %d, stderr %q", tc.args, code, stderr)
		}
	}
}
