package portal

import (
	"strings"

	"github.com/opragmatico/goscriber/internal/hotkey"
)

// keysyms are the xkbcommon names of the keys a Binding may have, other
// than letters and digits, which are their own names.
var keysyms = map[string]string{
	"space": "space", "return": "Return", "escape": "Escape", "tab": "Tab",
	"up": "Up", "down": "Down", "left": "Left", "right": "Right",
}

// Trigger turns a shortcut as settings store it into a trigger in the
// freedesktop shortcuts format, which the portal takes as a preferred
// trigger: "ctrl+alt+space" is "CTRL+ALT+space". It returns "" for a
// shortcut not to suggest: one that does not parse or cannot serve p, and
// a key without modifiers other than F1–F20, which the desktop would take
// from every app for as long as Goscriber runs; the cancel shortcut's
// plain Escape is one.
func Trigger(p hotkey.Purpose, s string) string {
	b, err := hotkey.ParseFor(s, p)
	if err != nil || (b.Mods == 0 && !b.IsFunctionKey()) {
		return ""
	}
	var parts []string
	for _, m := range []struct {
		mod  hotkey.Modifier
		name string
	}{
		{hotkey.ModCtrl, "CTRL"}, {hotkey.ModAlt, "ALT"}, {hotkey.ModShift, "SHIFT"}, {hotkey.ModSuper, "LOGO"},
	} {
		if b.Has(m.mod) {
			parts = append(parts, m.name)
		}
	}
	key, ok := keysyms[b.Key]
	switch {
	case ok:
	case b.IsFunctionKey():
		key = strings.ToUpper(b.Key)
	default:
		key = b.Key
	}
	return strings.Join(append(parts, key), "+")
}
