//go:build !darwin && !linux

package platform

import (
	"log/slog"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/output"
)

// Pasteboard is the clipboard. Only macOS and Linux are supported so far
// (Windows is issue #36); elsewhere every method fails with
// output.ErrUnsupported.
type Pasteboard struct{}

var _ output.Clipboard = Pasteboard{}

// GeneralPasteboard is the clipboard.
func GeneralPasteboard() Pasteboard { return Pasteboard{} }

func (Pasteboard) ChangeCount() int            { return 0 }
func (Pasteboard) Text() (string, bool)        { return "", false }
func (Pasteboard) SetText(string, bool) error  { return output.ErrUnsupported }
func (Pasteboard) Save() (func() error, error) { return nil, output.ErrUnsupported }

// Keyboard posts key presses to the focused app; only macOS and Linux are
// supported so far.
type Keyboard struct {
	Settings func() config.Settings
	Log      *slog.Logger
}

var _ output.Keyboard = Keyboard{}

func (Keyboard) Paste() error       { return output.ErrUnsupported }
func (Keyboard) PressReturn() error { return output.ErrUnsupported }
func (Keyboard) Type(string) error  { return output.ErrUnsupported }

// AccessibilityTrusted is a macOS permission; elsewhere it reports true.
func AccessibilityTrusted() bool { return true }

// PasteKeyName describes the paste shortcut.
func (Keyboard) PasteKeyName() string { return "Ctrl+V" }
