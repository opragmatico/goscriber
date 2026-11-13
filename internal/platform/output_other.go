//go:build !darwin

package platform

import "github.com/opragmatico/goscriber/internal/output"

// Pasteboard is the clipboard. Only macOS is supported so far (Linux and
// Windows are issues #35 and #36); elsewhere every method fails with
// output.ErrUnsupported.
type Pasteboard struct{}

var _ output.Clipboard = Pasteboard{}

// GeneralPasteboard is the clipboard.
func GeneralPasteboard() Pasteboard { return Pasteboard{} }

func (Pasteboard) ChangeCount() int            { return 0 }
func (Pasteboard) Text() (string, bool)        { return "", false }
func (Pasteboard) SetText(string, bool) error  { return output.ErrUnsupported }
func (Pasteboard) Save() (func() error, error) { return nil, output.ErrUnsupported }

// Keyboard posts key presses to the focused app; only macOS is supported
// so far.
type Keyboard struct{}

var _ output.Keyboard = Keyboard{}

func (Keyboard) Paste() error       { return output.ErrUnsupported }
func (Keyboard) PressReturn() error { return output.ErrUnsupported }
func (Keyboard) Type(string) error  { return output.ErrUnsupported }

// AccessibilityTrusted is a macOS permission; elsewhere it reports true.
func AccessibilityTrusted() bool { return true }

// PasteKeyName describes the paste shortcut.
func PasteKeyName() string { return "Ctrl+V" }
