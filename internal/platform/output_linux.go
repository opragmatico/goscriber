//go:build linux

package platform

import (
	"log/slog"
	"sync"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/output"
	"github.com/opragmatico/goscriber/internal/session"
	"github.com/opragmatico/goscriber/internal/typing"
)

// Pasteboard is the X11 clipboard, as plain text: a transcript goes on both
// CLIPBOARD (Ctrl+V) and PRIMARY (what Shift+Insert pastes in some
// terminals), as Handy does. Under Wayland it goes through XWayland, whose
// selections the compositor hands to Wayland apps. It implements
// output.Clipboard.
//
// X11 keeps no clipboard of its own: the app that copied holds the data.
// So Save keeps the text the clipboard offers, and restoring puts that text
// back with Goscriber holding it; rich text comes back plain, and an image
// or files copied before the dictation are not put back.
type Pasteboard struct {
	// Log receives what Save cannot keep. Nil means slog's default logger.
	Log *slog.Logger
}

var _ output.Clipboard = Pasteboard{}

// GeneralPasteboard is the clipboard.
func GeneralPasteboard() Pasteboard { return Pasteboard{} }

func (p Pasteboard) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// ChangeCount implements output.Clipboard: it changes when Goscriber writes
// CLIPBOARD and when another app takes it from Goscriber.
func (Pasteboard) ChangeCount() int {
	c, err := theClipboard()
	if err != nil {
		return 0
	}
	changes, _ := c.counts()
	return int(changes)
}

// Text returns the clipboard's text; ok is false when it holds none or
// cannot be read.
func (Pasteboard) Text() (string, bool) {
	text, held, err := readSelection("CLIPBOARD")
	return text, held == selText && err == nil
}

// SetText implements output.Clipboard. Transient text carries KDE's
// x-kde-passwordManagerHint, which tells clipboard managers to leave it
// out of their history.
func (Pasteboard) SetText(text string, transient bool) error {
	c, err := theClipboard()
	if err != nil {
		return err
	}
	return c.set(maskClipboard|maskPrimary, text, transient)
}

// Save implements output.Clipboard for the text of CLIPBOARD and PRIMARY.
// It fails only without an X server. A clipboard it cannot keep (its app
// does not answer, or it holds an image or files rather than text) is left
// with the transcript, and the paste goes on. PRIMARY is put back only
// while Goscriber still holds it, so a selection the user made since is
// kept.
func (p Pasteboard) Save() (func() error, error) {
	c, err := theClipboard()
	if err != nil {
		return nil, err
	}
	type saved struct {
		text string
		held selection
		keep bool // it cannot be put back
	}
	read := func(name string) saved {
		text, held, err := readSelection(name)
		switch {
		case err != nil:
			p.log().Warn("the clipboard cannot be read, so it will not be put back after the paste", "selection", name, "err", err)
			return saved{keep: true}
		case held == selOther:
			p.log().Info("the clipboard holds no text (an image or files?), which Goscriber cannot put back on Linux", "selection", name)
			return saved{keep: true}
		case len(text) > c.maxText():
			p.log().Warn("the clipboard holds too much text to put back after the paste", "selection", name, "bytes", len(text))
			return saved{keep: true}
		}
		return saved{text: text, held: held}
	}
	clipboard, primary := read("CLIPBOARD"), read("PRIMARY")
	put := func(mask int, s saved) error {
		switch {
		case s.keep:
			return nil
		case s.held == selText:
			return c.set(mask, s.text, false)
		default:
			c.clear(mask)
			return nil
		}
	}
	return func() error {
		err := put(maskClipboard, clipboard)
		if c.ownsPrimary() {
			_ = put(maskPrimary, primary) // best effort
		}
		return err
	}, nil
}

// keyPresser presses keys for the transcript: the built-in X11 key events
// or a typing tool.
type keyPresser interface {
	Paste(keys config.PasteKeys) error
	Type(text string) error
	PressReturn() error
}

// Keyboard presses keys in the focused app with the typing_tool setting:
// auto uses X11 key events (XTEST) in an X11 session and, on Wayland, the
// first of wtype, dotool and ydotool that works (internal/typing). It
// implements output.Keyboard.
type Keyboard struct {
	// Settings supplies typing_tool and paste_keys at each call; nil means
	// the defaults.
	Settings func() config.Settings
	// Log is unused on Linux.
	Log *slog.Logger
}

var _ output.Keyboard = Keyboard{}

// waylandTools is the auto typing tool on Wayland. It is shared, so the
// tool that worked is tried first by every Keyboard.
var waylandTools = sync.OnceValue(func() *typing.Chain {
	return typing.NewChain(typing.WaylandTools, nil, nil)
})

func (k Keyboard) settings() config.Settings {
	if k.Settings != nil {
		return k.Settings()
	}
	return config.Defaults()
}

// presser is what presses keys for the settings s in this session.
func presser(s config.Settings, kind session.Kind) (keyPresser, error) {
	if s.TypingTool != config.TypingAuto {
		return typing.Tool{Name: s.TypingTool}, nil
	}
	if kind == session.Wayland {
		return waylandTools(), nil
	}
	if err := xtestAvailable(); err != nil {
		return nil, err
	}
	return xtest{}, nil
}

func (k Keyboard) do(f func(keyPresser, config.Settings) error) error {
	s := k.settings()
	p, err := presser(s, session.Current())
	if err != nil {
		return err
	}
	return f(p, s)
}

// Paste implements output.Keyboard with the paste_keys setting.
func (k Keyboard) Paste() error {
	return k.do(func(p keyPresser, s config.Settings) error { return p.Paste(s.PasteKeys) })
}

// PressReturn implements output.Keyboard.
func (k Keyboard) PressReturn() error {
	return k.do(func(p keyPresser, _ config.Settings) error { return p.PressReturn() })
}

// Type implements output.Keyboard.
func (k Keyboard) Type(text string) error {
	return k.do(func(p keyPresser, _ config.Settings) error { return p.Type(text) })
}

// PasteKeyName describes the paste keys and what presses them, such as
// "Shift+Insert (X11 key events)", for diagnostics.
func (k Keyboard) PasteKeyName() string {
	s := k.settings()
	keys := map[config.PasteKeys]string{
		config.PasteCtrlV: "Ctrl+V", config.PasteShiftInsert: "Shift+Insert", config.PasteCtrlShiftV: "Ctrl+Shift+V",
	}[s.PasteKeys]
	p, err := presser(s, session.Current())
	var how string
	switch p := p.(type) {
	case xtest:
		how = "X11 key events"
	case typing.Tool:
		how = string(p.Name)
	case *typing.Chain:
		how = "the first of " + typing.Names(typing.WaylandTools) + " that works"
	default:
		how = "nothing: " + err.Error()
	}
	return keys + " (" + how + ")"
}

// AccessibilityTrusted is a macOS permission; Linux has none, so it
// reports true.
func AccessibilityTrusted() bool { return true }
