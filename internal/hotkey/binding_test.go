package hotkey

import (
	"errors"
	"strings"
	"testing"

	"github.com/opragmatico/goscriber/internal/config"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		in     string
		want   Binding
		darwin string // Format("darwin")
		linux  string // Format("linux")
	}{
		{"option+space", Binding{ModAlt, "space"}, "option+space", "alt+space"},
		{"Ctrl+Shift+D", Binding{ModCtrl | ModShift, "d"}, "ctrl+shift+d", "ctrl+shift+d"},
		{"shift + ctrl + d", Binding{ModCtrl | ModShift, "d"}, "ctrl+shift+d", "ctrl+shift+d"},
		{"cmd+shift+space", Binding{ModShift | ModSuper, "space"}, "shift+cmd+space", "shift+super+space"},
		{"ctrl+alt+space", Binding{ModCtrl | ModAlt, "space"}, "ctrl+option+space", "ctrl+alt+space"},
		{"control+opt+command+enter", Binding{ModCtrl | ModAlt | ModSuper, "return"}, "ctrl+option+cmd+return", "ctrl+alt+super+return"},
		{"win+f12", Binding{ModSuper, "f12"}, "cmd+f12", "super+f12"},
		{"F5", Binding{0, "f5"}, "f5", "f5"},
		{"esc", Binding{0, "escape"}, "escape", "escape"},
		{"meta+9", Binding{ModSuper, "9"}, "cmd+9", "super+9"},
	} {
		got, err := Parse(tc.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Parse(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
		if f := got.Format("darwin"); f != tc.darwin {
			t.Errorf("Parse(%q).Format(darwin) = %q, want %q", tc.in, f, tc.darwin)
		}
		if f := got.Format("linux"); f != tc.linux {
			t.Errorf("Parse(%q).Format(linux) = %q, want %q", tc.in, f, tc.linux)
		}
		// What Format writes, Parse reads back.
		if again, err := Parse(got.String()); err != nil || again != got {
			t.Errorf("round trip of %q: %+v, %v", got.String(), again, err)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "empty shortcut"},
		{"  ", "empty shortcut"},
		{"ctrl++a", "empty key name"},
		{"ctrl+", "empty key name"},
		{"ctrl+ctrl+a", "ctrl appears twice"},
		{"option+alt+a", "alt appears twice"},
		{"a+b", "one key besides its modifiers, not a and b"},
		{"ctrl+hyper", `unknown key "hyper"`},
		{"ctrl+comma", `unknown key "comma"`},
	} {
		_, err := Parse(tc.in)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Parse(%q) error %v, want it to mention %q", tc.in, err, tc.want)
		}
	}
	for _, in := range []string{"option", "ctrl+shift", "fn", "right_option", "RightAlt", "fn+space"} {
		if _, err := Parse(in); !errors.Is(err, ErrModifierOnly) {
			t.Errorf("Parse(%q) = %v, want ErrModifierOnly", in, err)
		}
	}
}

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		in         string
		transcribe bool
		cancel     bool
	}{
		{"option+space", true, true},
		{"f5", true, true},
		{"escape", false, true},
		{"space", false, false},
		{"d", false, false},
		{"7", false, false},
		{"return", false, false},
		{"shift+escape", true, true},
	} {
		b, err := Parse(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Validate(PurposeTranscribe); (err == nil) != tc.transcribe {
			t.Errorf("%s as transcribe: %v", tc.in, err)
		}
		if err := b.Validate(PurposeCancel); (err == nil) != tc.cancel {
			t.Errorf("%s as cancel: %v", tc.in, err)
		}
	}
	if _, err := ParseFor("space", PurposeTranscribe); err == nil || !strings.Contains(err.Error(), "add a modifier") {
		t.Errorf("ParseFor(space) = %v", err)
	}
}

// Every OS's default shortcuts parse and are valid for their purpose.
func TestDefaultShortcutsAreValid(t *testing.T) {
	d := config.Defaults()
	if _, err := ParseFor(d.TranscribeShortcut, PurposeTranscribe); err != nil {
		t.Error(err)
	}
	if _, err := ParseFor(d.CancelShortcut, PurposeCancel); err != nil {
		t.Error(err)
	}
	for _, s := range []string{"option+space", "ctrl+space", "ctrl+alt+space", "alt+space"} {
		if _, err := ParseFor(s, PurposeTranscribe); err != nil {
			t.Error(err)
		}
	}
}
