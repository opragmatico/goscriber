package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/hotkey"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/session"
	"github.com/opragmatico/goscriber/internal/ui"
)

// desktopShortcuts is a shortcut backend whose keys the desktop owns: its
// global shortcuts portal, on Wayland.
type desktopShortcuts interface {
	hotkey.Backend
	// Configure opens the desktop's dialog to change the keys.
	Configure() error
	// Close gives the keys back to the desktop.
	Close() error
}

// desktopShortcutsOptions configure openDesktopShortcuts.
type desktopShortcutsOptions struct {
	// Suggest returns the shortcut to suggest to the desktop for a
	// purpose, as settings store it.
	Suggest func(hotkey.Purpose) string
	// OnStatus is told each change in what the desktop bound.
	OnStatus func(ui.DesktopShortcuts)
	Log      *slog.Logger
}

// portalTimeout bounds connecting to the desktop's portal and each of its
// calls that waits for no user.
const portalTimeout = 5 * time.Second

// hotkeyBackend is what registers the shortcuts. On Wayland it is the
// desktop's global shortcuts portal when the desktop has one; otherwise,
// and everywhere else, the system's backend, which on Wayland hears the
// shortcut in X11 windows only.
func (sh *shell) hotkeyBackend() hotkey.Backend {
	if sh.session != session.Wayland || openDesktopShortcuts == nil {
		return newHotkeyBackend()
	}
	return &waylandShortcuts{sh: sh}
}

// waylandShortcuts chooses the backend at the first Register, which runs
// on the hotkey manager's goroutine: waiting for the portal holds up
// neither the launch nor the UI.
type waylandShortcuts struct {
	sh   *shell
	once sync.Once
	be   hotkey.Backend
}

func (w *waylandShortcuts) Register(p hotkey.Purpose, b hotkey.Binding) (hotkey.Registration, error) {
	w.once.Do(func() { w.be = w.sh.openShortcutPortal() })
	return w.be.Register(p, b)
}

// openShortcutPortal asks the desktop's portal to bind the shortcuts, or
// returns the system's backend when the desktop has no portal.
func (sh *shell) openShortcutPortal() hotkey.Backend {
	ctx, cancel := context.WithTimeout(context.Background(), portalTimeout)
	defer cancel()
	ds, err := openDesktopShortcuts(ctx, desktopShortcutsOptions{
		Suggest:  func(p hotkey.Purpose) string { return suggestedShortcut(sh.rt.Settings.Get(), p) },
		OnStatus: sh.desktopShortcutsChanged,
		Log:      sh.rt.Log.Logger,
	})
	if err != nil {
		sh.rt.Log.Info("Wayland session: the shortcut is heard in X11 windows only; bind '"+ui.ToggleCommand+
			"' to a key in the desktop's keyboard settings to dictate anywhere", "portal", err)
		return newHotkeyBackend()
	}
	sh.portalMu.Lock()
	defer sh.portalMu.Unlock()
	if sh.portalClosed { // the app stopped meanwhile
		_ = ds.Close()
	} else {
		sh.portal = ds
	}
	return ds
}

// suggestedShortcut is the shortcut the settings give purpose p.
func suggestedShortcut(s config.Settings, p hotkey.Purpose) string {
	switch p {
	case hotkey.PurposeTranscribe:
		return s.TranscribeShortcut
	case hotkey.PurposePostProcess:
		return s.PostProcessShortcut
	case hotkey.PurposeCancel:
		return s.CancelShortcut
	default:
		return ""
	}
}

// desktopShortcutsChanged shows what the desktop bound, and the problem
// when it bound nothing. It runs on the portal's goroutines.
func (sh *shell) desktopShortcutsChanged(d ui.DesktopShortcuts) {
	switch d.State {
	case ui.PortalBound:
		sh.notices.Resolve(notice.CodeShortcutFailed)
	case ui.PortalRefused, ui.PortalFailed:
		n := notice.New(notice.CodeShortcutFailed, d.Text())
		n.Action = notice.Action{Label: "Open Settings", ID: notice.ActionOpenSettings}
		sh.notices.Publish(n)
	}
	sh.do(func() { sh.settings.SetDesktopShortcuts(d) })
}

// changeDesktopShortcuts opens the desktop's dialog to change the keys.
func (sh *shell) changeDesktopShortcuts() error {
	sh.portalMu.Lock()
	ds := sh.portal
	sh.portalMu.Unlock()
	if ds == nil {
		return errors.New("the desktop has no global shortcuts portal")
	}
	return ds.Configure()
}

// closeShortcutPortal gives the keys back to the desktop as the app stops.
func (sh *shell) closeShortcutPortal() {
	sh.portalMu.Lock()
	ds := sh.portal
	sh.portal, sh.portalClosed = nil, true
	sh.portalMu.Unlock()
	if ds != nil {
		if err := ds.Close(); err != nil {
			sh.rt.Log.Debug("could not close the shortcuts portal", "err", err)
		}
	}
}
