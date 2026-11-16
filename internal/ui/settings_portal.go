package ui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// PortalState is where the desktop's global shortcuts portal stands with
// Goscriber's shortcuts, on Wayland.
type PortalState int

// Portal states.
const (
	// PortalNone: no portal holds the shortcuts, so the desktop's own
	// shortcut runs ToggleCommand. It is also the state until the app
	// knows whether the desktop has a portal.
	PortalNone PortalState = iota
	// PortalAsking: the desktop is asking the user to confirm the keys.
	PortalAsking
	// PortalBound: the desktop bound the shortcuts.
	PortalBound
	// PortalRefused: the user dismissed the desktop's dialog.
	PortalRefused
	// PortalFailed: the portal could not bind the shortcuts.
	PortalFailed
)

// DesktopShortcuts is what the desktop's global shortcuts portal did with
// Goscriber's shortcuts.
type DesktopShortcuts struct {
	State PortalState
	// Transcribe and Cancel are the keys the desktop bound, in its own
	// words, such as "Ctrl+Alt+Space"; "" when it bound none.
	Transcribe, Cancel string
	// Problem is why the shortcuts are not bound, when State is
	// PortalRefused or PortalFailed.
	Problem string
	// CanChange is whether the desktop can show its dialog to change the
	// keys, which ChangeDesktopShortcuts opens.
	CanChange bool
}

// Hints of the shortcut rows.
const (
	dictationHint        = "Starts and stops dictation, in any app. Click it, then press the keys."
	dictationHintCommand = WaylandShortcutNote + " The shortcut set here is heard only in X11 apps; Activation and Cancel apply to it alone."
	// DictationHintPortal is the dictation row's hint while the desktop's
	// portal holds the shortcuts.
	DictationHintPortal = "Suggested to your desktop when Goscriber first asked for its shortcuts. " +
		"The desktop keeps the keys you confirmed, shown under Desktop keys."
	cancelHint = "Discards the recording. Taken from other apps only while recording."
	// CancelHintPortal is the cancel row's hint while the desktop's portal
	// holds the shortcuts.
	CancelHintPortal = "Discards the recording. A key without modifiers, such as Escape, is not suggested to the desktop, " +
		"which would take it from every app; give Cancel keys in the desktop's settings instead."
)

// desktopKeysView is the General page's view of what the portal bound, on
// Wayland.
type desktopKeysView struct {
	dictationHint, cancelHint *widget.Label
	keys                      *widget.Label
	change                    *widget.Button
	row                       fyne.CanvasObject
	// page is the General page's content, laid out again when the row
	// shows or hides and the hints change length.
	page fyne.CanvasObject
}

// newDesktopKeysView builds the Desktop keys row and the hints that
// change with the portal.
func (w *SettingsWindow) newDesktopKeysView() *desktopKeysView {
	v := &desktopKeysView{
		dictationHint: note(""),
		cancelHint:    note(""),
		keys:          paragraph(""),
	}
	v.change = widget.NewButton("Change…", func() {
		if w.opts.ChangeDesktopShortcuts == nil {
			return
		}
		if err := w.opts.ChangeDesktopShortcuts(); err != nil {
			v.keys.SetText(w.desktop.Text() + " " + sentenceCase(strings.TrimSuffix(err.Error(), ".")) + ".")
		}
	})
	v.row = w.row("Desktop keys", "", container.NewVBox(v.keys, container.NewHBox(v.change)))
	w.keysView = v
	w.refreshDesktopKeys()
	return v
}

// SetDesktopShortcuts shows what the desktop's global shortcuts portal did
// with Goscriber's shortcuts. Call it on the main thread.
func (w *SettingsWindow) SetDesktopShortcuts(d DesktopShortcuts) {
	w.desktop = d
	w.refreshDesktopKeys()
	if w.permBox != nil {
		w.permBox.Objects = w.permissionRows()
		w.permBox.Refresh()
	}
	w.refreshOnboarding()
}

func (w *SettingsWindow) refreshDesktopKeys() {
	v := w.keysView
	if v == nil {
		return
	}
	d := w.desktop
	if v.page != nil {
		defer v.page.Refresh()
	}
	if d.State == PortalNone {
		v.dictationHint.SetText(dictationHintCommand)
		v.cancelHint.SetText(cancelHint)
		v.row.Hide()
		return
	}
	v.dictationHint.SetText(DictationHintPortal)
	v.cancelHint.SetText(CancelHintPortal)
	v.keys.SetText(d.Text())
	if d.State == PortalBound && d.CanChange && w.opts.ChangeDesktopShortcuts != nil {
		v.change.Show()
	} else {
		v.change.Hide()
	}
	v.row.Show()
}

// Text says what the portal bound, or why it did not.
func (d DesktopShortcuts) Text() string {
	keys := func(k string) string {
		if k == "" {
			return "no keys"
		}
		return k
	}
	switch d.State {
	case PortalAsking:
		return "Waiting for you to confirm Goscriber's shortcuts in the desktop's dialog."
	case PortalBound:
		s := fmt.Sprintf("Dictation: %s. Cancel: %s.", keys(d.Transcribe), keys(d.Cancel))
		if !d.CanChange {
			s += " Change them in the desktop's keyboard settings."
		}
		return s
	case PortalRefused:
		return "You dismissed the desktop's dialog, so no shortcut is bound. Restart Goscriber to be asked again, " +
			"or add a shortcut in the desktop's keyboard settings that runs " + ToggleCommand + "."
	case PortalFailed:
		return "The desktop could not bind the shortcuts: " + d.Problem + ". Add a shortcut in the desktop's keyboard settings " +
			"that runs " + ToggleCommand + " instead."
	default:
		return WaylandShortcutNote
	}
}

// waylandShortcutNote is the Permissions page's note on the dictation
// shortcut, on Wayland.
func (w *SettingsWindow) waylandShortcutNote() string {
	if w.desktop.State == PortalNone {
		return WaylandShortcutNote
	}
	return "Your desktop binds Goscriber's shortcuts through its global shortcuts portal. " + w.desktop.Text()
}

// TryHintPortalAsking is the onboarding try step's hint while the
// desktop asks the user to confirm the shortcuts.
const TryHintPortalAsking = "Confirm Goscriber's shortcuts in the desktop's dialog first. Then click in the box below, " +
	"press the dictation shortcut, say a sentence, and press it again."

// tryStepShortcut is the onboarding try step's hint and the shortcut on
// the line below it, on Wayland.
func (w *SettingsWindow) tryStepShortcut() (hint, key string) {
	d := w.desktop
	switch {
	case d.State == PortalAsking:
		return TryHintPortalAsking, ""
	case d.State == PortalBound && d.Transcribe != "":
		return tryHint(w.cur.ActivationMode), d.Transcribe
	case d.State == PortalBound:
		return "The desktop bound no keys for dictation: give it some in the desktop's keyboard settings, then click in the box below, " +
			"press them, say a sentence, and press them again.", ""
	default:
		return TryHintWayland, ToggleCommand
	}
}

// sentenceCase capitalises the first letter of s.
func sentenceCase(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
