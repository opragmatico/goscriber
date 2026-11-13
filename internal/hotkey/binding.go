package hotkey

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
)

// Modifier is a modifier key of a Binding.
type Modifier uint8

// Modifiers, in the order a Binding lists them.
const (
	ModCtrl  Modifier = 1 << iota // Control
	ModAlt                        // Option on macOS, Alt elsewhere
	ModShift                      // Shift
	ModSuper                      // Command on macOS, the Windows or Super key elsewhere
)

var modifierOrder = []Modifier{ModCtrl, ModAlt, ModShift, ModSuper}

// modifierNames maps every accepted spelling to its modifier.
var modifierNames = map[string]Modifier{
	"ctrl":    ModCtrl,
	"control": ModCtrl,
	"alt":     ModAlt,
	"option":  ModAlt,
	"opt":     ModAlt,
	"shift":   ModShift,
	"cmd":     ModSuper,
	"command": ModSuper,
	"super":   ModSuper,
	"win":     ModSuper,
	"meta":    ModSuper,
}

// modifierOnly are keys people try to bind on their own, which need a
// keyboard event tap of their own (issue #17): they are refused with a
// clear message rather than as unknown keys.
var modifierOnly = []string{
	"fn", "globe", "rightoption", "right_option", "roption", "rightalt", "right_alt", "ralt",
	"leftoption", "left_option", "rightcmd", "right_cmd", "rightcommand", "rightctrl", "right_ctrl",
	"rightshift", "right_shift", "capslock", "caps_lock",
}

// keyNames maps every accepted spelling of a key to its canonical name.
var keyNames = func() map[string]string {
	m := map[string]string{
		"space":  "space",
		"return": "return", "enter": "return",
		"escape": "escape", "esc": "escape",
		"tab": "tab",
		"up":  "up", "down": "down", "left": "left", "right": "right",
	}
	for c := 'a'; c <= 'z'; c++ {
		m[string(c)] = string(c)
	}
	for c := '0'; c <= '9'; c++ {
		m[string(c)] = string(c)
	}
	for i := 1; i <= 20; i++ {
		f := fmt.Sprintf("f%d", i)
		m[f] = f
	}
	return m
}()

// Binding is a global shortcut: modifiers and one key, such as Option+Space.
// Its zero value is no shortcut.
type Binding struct {
	Mods Modifier
	// Key is the key's canonical name: "a"–"z", "0"–"9", "f1"–"f20",
	// "space", "return", "escape", "tab", "up", "down", "left" or "right".
	Key string
}

// IsZero reports whether b is no shortcut.
func (b Binding) IsZero() bool { return b.Key == "" }

// Has reports whether b includes the modifier m.
func (b Binding) Has(m Modifier) bool { return b.Mods&m != 0 }

// String formats b the way settings store it on this OS, as Parse reads
// it: "option+space", "ctrl+shift+d", "cmd+f5".
func (b Binding) String() string { return b.Format(runtime.GOOS) }

// Format formats b with goos's modifier names: Option and Command on
// macOS, Alt and Super elsewhere. Modifiers come first, in the order Ctrl,
// Alt, Shift, Super.
func (b Binding) Format(goos string) string {
	if b.IsZero() {
		return ""
	}
	var parts []string
	for _, m := range modifierOrder {
		if b.Has(m) {
			parts = append(parts, modifierName(m, goos))
		}
	}
	return strings.Join(append(parts, b.Key), "+")
}

func modifierName(m Modifier, goos string) string {
	mac := goos == "darwin"
	switch m {
	case ModCtrl:
		return "ctrl"
	case ModAlt:
		if mac {
			return "option"
		}
		return "alt"
	case ModShift:
		return "shift"
	default:
		if mac {
			return "cmd"
		}
		return "super"
	}
}

// IsFunctionKey reports whether b's key is F1–F20, which nothing types, so
// it may be bound without modifiers.
func (b Binding) IsFunctionKey() bool {
	return len(b.Key) >= 2 && b.Key[0] == 'f' && b.Key[1] >= '0' && b.Key[1] <= '9'
}

// ErrModifierOnly is returned (wrapped) by Parse for a shortcut that is a
// modifier on its own, such as the right Option key or Fn. Goscriber's
// shortcuts are a key with optional modifiers; modifier-only shortcuts need
// a keyboard event tap of their own and are not supported yet.
var ErrModifierOnly = errors.New("modifier-only shortcuts such as right Option or Fn are not supported yet; add a key, such as option+space")

// Parse reads a shortcut such as "option+space", "Ctrl+Shift+D" or "f5":
// modifiers and exactly one key joined by "+", in any case and order, with
// spaces around the "+" allowed. See Binding for the keys; modifiers are
// ctrl (control), alt (option, opt), shift and cmd (command, super, win,
// meta). Parse checks the syntax only: Validate checks what a shortcut may
// be used for.
func Parse(s string) (Binding, error) {
	if strings.TrimSpace(s) == "" {
		return Binding{}, errors.New("hotkey: empty shortcut")
	}
	var b Binding
	for part := range strings.SplitSeq(s, "+") {
		name := strings.ToLower(strings.TrimSpace(part))
		switch {
		case name == "":
			return Binding{}, fmt.Errorf("hotkey: %q: empty key name", s)
		case modifierNames[name] != 0:
			m := modifierNames[name]
			if b.Has(m) {
				return Binding{}, fmt.Errorf("hotkey: %q: %s appears twice", s, name)
			}
			b.Mods |= m
		case keyNames[name] != "":
			if b.Key != "" {
				return Binding{}, fmt.Errorf("hotkey: %q: a shortcut has one key besides its modifiers, not %s and %s", s, b.Key, name)
			}
			b.Key = keyNames[name]
		case slices.Contains(modifierOnly, name):
			return Binding{}, fmt.Errorf("hotkey: %q: %w", s, ErrModifierOnly)
		default:
			return Binding{}, fmt.Errorf("hotkey: %q: unknown key %q", s, name)
		}
	}
	if b.Key == "" {
		return Binding{}, fmt.Errorf("hotkey: %q: %w", s, ErrModifierOnly)
	}
	return b, nil
}

// Purpose is what a shortcut is for.
type Purpose string

// Purposes.
const (
	PurposeTranscribe Purpose = "transcribe" // starts and stops dictation, registered all the time
	PurposeCancel     Purpose = "cancel"     // discards the recording, registered only while recording
)

// Validate checks that b may serve purpose. A global shortcut swallows its
// key in every app, so a plain letter, digit or Space would make it
// impossible to type: a shortcut needs a modifier unless its key is F1–F20.
// The cancel shortcut may also be a plain Escape, because it is only
// registered while recording.
func (b Binding) Validate(p Purpose) error {
	switch {
	case b.IsZero():
		return errors.New("hotkey: empty shortcut")
	case b.Mods != 0, b.IsFunctionKey():
		return nil
	case p == PurposeCancel && b.Key == "escape":
		return nil
	case b.Key == "escape":
		return fmt.Errorf("hotkey: %s: a plain Escape is only allowed as the cancel shortcut; add a modifier", b)
	default:
		return fmt.Errorf("hotkey: %s: add a modifier, or every %s typed in any app would trigger it", b, b.Key)
	}
}

// ParseFor parses s and validates it for purpose.
func ParseFor(s string, p Purpose) (Binding, error) {
	b, err := Parse(s)
	if err != nil {
		return Binding{}, err
	}
	if err := b.Validate(p); err != nil {
		return Binding{}, err
	}
	return b, nil
}
