package ui

import (
	"fmt"
	"runtime"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/hotkey"
)

// ShortcutInput records a key combination for a global shortcut: tap it,
// hold the modifiers and press the key, and it reports the shortcut the way
// settings store it, such as "option+space". It accepts only what
// internal/hotkey would register for its purpose: a plain letter, for
// example, is refused with the reason.
type ShortcutInput struct {
	widget.Button

	purpose   hotkey.Purpose
	value     string
	recording bool
	mods      hotkey.Modifier

	// Check, if set, refuses a valid shortcut with an error, such as one
	// the other shortcut already uses.
	Check func(hotkey.Binding) error
	// OnChanged receives each shortcut recorded.
	OnChanged func(shortcut string)
	// OnRecording is told when the input starts and stops listening.
	OnRecording func(recording bool)
	// OnError receives why the keys pressed cannot be the shortcut, and ""
	// when that no longer applies.
	OnError func(msg string)
}

// ShortcutPrompt is what the input says while it listens.
const ShortcutPrompt = "Press a shortcut…"

// NewShortcutInput returns an input for a shortcut serving purpose.
func NewShortcutInput(purpose hotkey.Purpose) *ShortcutInput {
	in := &ShortcutInput{purpose: purpose}
	in.ExtendBaseWidget(in)
	return in
}

// Shortcut is the shortcut shown, as settings store it.
func (in *ShortcutInput) Shortcut() string { return in.value }

// SetShortcut shows s. It reports nothing to OnChanged.
func (in *ShortcutInput) SetShortcut(s string) {
	in.value = s
	if !in.recording {
		in.SetText(ShortcutLabel(s))
	}
}

// Recording reports whether the input is listening for keys.
func (in *ShortcutInput) Recording() bool { return in.recording }

// Tapped starts listening for a key combination.
func (in *ShortcutInput) Tapped(*fyne.PointEvent) {
	if in.Disabled() || in.recording {
		return
	}
	in.recording, in.mods = true, 0
	in.Importance = widget.HighImportance
	in.SetText(ShortcutPrompt)
	in.report("")
	if in.OnRecording != nil {
		in.OnRecording(true)
	}
	if c := fyne.CurrentApp().Driver().CanvasForObject(in); c != nil && c.Focused() != in {
		c.Focus(in)
	}
}

// stop stops listening and shows the shortcut again.
func (in *ShortcutInput) stop() {
	if !in.recording {
		return
	}
	in.recording, in.mods = false, 0
	in.Importance = widget.MediumImportance
	in.SetText(ShortcutLabel(in.value))
	if in.OnRecording != nil {
		in.OnRecording(false)
	}
	if c := fyne.CurrentApp().Driver().CanvasForObject(in); c != nil && c.Focused() == in {
		c.Unfocus()
	}
}

// KeyDown follows the modifiers held and records the shortcut when a key
// goes down with them. Escape on its own stops listening without a change,
// unless Escape alone is a valid shortcut here, as it is for cancelling.
func (in *ShortcutInput) KeyDown(e *fyne.KeyEvent) {
	if !in.recording {
		return
	}
	if m, ok := modifierKeys[e.Name]; ok {
		in.mods |= m
		in.SetText(modifiersLabel(in.mods) + "…")
		return
	}
	key, err := hotkey.Parse(string(e.Name))
	if e.Name == fyne.KeyEnter {
		key, err = hotkey.Binding{Key: "return"}, nil
	}
	if err != nil || key.Mods != 0 {
		in.report(fmt.Sprintf("%s cannot be part of a shortcut.", e.Name))
		return
	}
	b := hotkey.Binding{Mods: in.mods, Key: key.Key}
	err = b.Validate(in.purpose)
	if err != nil && b.Mods == 0 && b.Key == "escape" {
		in.report("")
		in.stop()
		return
	}
	if err == nil && in.Check != nil {
		err = in.Check(b)
	}
	if err != nil {
		in.report(strings.TrimPrefix(err.Error(), "hotkey: "))
		return
	}
	in.value = b.String()
	in.report("")
	in.stop()
	if in.OnChanged != nil {
		in.OnChanged(in.value)
	}
}

// KeyUp follows the modifiers released.
func (in *ShortcutInput) KeyUp(e *fyne.KeyEvent) {
	if m, ok := modifierKeys[e.Name]; ok && in.recording {
		in.mods &^= m
		if in.mods == 0 {
			in.SetText(ShortcutPrompt)
		} else {
			in.SetText(modifiersLabel(in.mods) + "…")
		}
	}
}

// TypedKey keeps Space and Return from pressing the button while it
// listens.
func (in *ShortcutInput) TypedKey(e *fyne.KeyEvent) {
	if !in.recording {
		in.Button.TypedKey(e)
	}
}

// FocusLost stops listening: the user clicked elsewhere.
func (in *ShortcutInput) FocusLost() {
	in.stop()
	in.Button.FocusLost()
}

func (in *ShortcutInput) report(msg string) {
	if in.OnError != nil {
		in.OnError(msg)
	}
}

var _ desktop.Keyable = (*ShortcutInput)(nil)

// modifierKeys are the modifier keys and what they add to a shortcut.
var modifierKeys = map[fyne.KeyName]hotkey.Modifier{
	desktop.KeyShiftLeft:    hotkey.ModShift,
	desktop.KeyShiftRight:   hotkey.ModShift,
	desktop.KeyControlLeft:  hotkey.ModCtrl,
	desktop.KeyControlRight: hotkey.ModCtrl,
	desktop.KeyAltLeft:      hotkey.ModAlt,
	desktop.KeyAltRight:     hotkey.ModAlt,
	desktop.KeySuperLeft:    hotkey.ModSuper,
	desktop.KeySuperRight:   hotkey.ModSuper,
}

// ShortcutLabel writes a shortcut as settings store it the way this OS
// shows shortcuts: "⌥ Space" on macOS, "Ctrl+Alt+Space" elsewhere. A
// shortcut that does not parse is shown as it is, and none as "None".
func ShortcutLabel(s string) string { return shortcutLabel(s, runtime.GOOS) }

func shortcutLabel(s, goos string) string {
	if strings.TrimSpace(s) == "" {
		return "None"
	}
	b, err := hotkey.Parse(s)
	if err != nil {
		return s
	}
	key := keyLabel(b.Key)
	if goos == "darwin" {
		if b.Mods == 0 {
			return key
		}
		return modifierSymbols(b.Mods) + " " + key
	}
	var parts []string
	for _, m := range []struct {
		mod  hotkey.Modifier
		name string
	}{{hotkey.ModCtrl, "Ctrl"}, {hotkey.ModAlt, "Alt"}, {hotkey.ModShift, "Shift"}, {hotkey.ModSuper, superName(goos)}} {
		if b.Has(m.mod) {
			parts = append(parts, m.name)
		}
	}
	return strings.Join(append(parts, key), "+")
}

// modifiersLabel writes modifiers held while recording.
func modifiersLabel(m hotkey.Modifier) string {
	if runtime.GOOS == "darwin" {
		return modifierSymbols(m)
	}
	return strings.TrimSuffix(shortcutLabel(hotkey.Binding{Mods: m, Key: "space"}.String(), runtime.GOOS), "Space")
}

// modifierSymbols writes modifiers in macOS's order and symbols.
func modifierSymbols(m hotkey.Modifier) string {
	var sb strings.Builder
	for _, s := range []struct {
		mod hotkey.Modifier
		sym string
	}{{hotkey.ModCtrl, "⌃"}, {hotkey.ModAlt, "⌥"}, {hotkey.ModShift, "⇧"}, {hotkey.ModSuper, "⌘"}} {
		if m&s.mod != 0 {
			sb.WriteString(s.sym)
		}
	}
	return sb.String()
}

func superName(goos string) string {
	if goos == "windows" {
		return "Win"
	}
	return "Super"
}

func keyLabel(key string) string {
	switch key {
	case "space", "return", "tab":
		return strings.ToUpper(key[:1]) + key[1:]
	case "escape":
		return "Esc"
	case "up":
		return "↑"
	case "down":
		return "↓"
	case "left":
		return "←"
	case "right":
		return "→"
	default:
		return strings.ToUpper(key)
	}
}
