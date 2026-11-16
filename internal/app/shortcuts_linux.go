//go:build linux

package app

import (
	"context"

	"github.com/opragmatico/goscriber/internal/hotkey"
	"github.com/opragmatico/goscriber/internal/hotkey/portal"
	"github.com/opragmatico/goscriber/internal/ui"
)

// openDesktopShortcuts opens the XDG desktop portal's GlobalShortcuts,
// which KDE Plasma, Hyprland and GNOME 48 and later provide.
var openDesktopShortcuts = func(ctx context.Context, opts desktopShortcutsOptions) (desktopShortcuts, error) {
	b, err := portal.Open(ctx, portal.Options{
		Suggest: opts.Suggest,
		OnStatus: func(s portal.Status) {
			if opts.OnStatus != nil {
				opts.OnStatus(desktopShortcutsFrom(s))
			}
		},
		Timeout: portalTimeout,
		Log:     opts.Log,
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

// desktopShortcutsFrom is what the settings window shows of the portal's
// status.
func desktopShortcutsFrom(s portal.Status) ui.DesktopShortcuts {
	d := ui.DesktopShortcuts{
		Transcribe: s.Triggers[hotkey.PurposeTranscribe],
		Cancel:     s.Triggers[hotkey.PurposeCancel],
		CanChange:  s.Configurable,
	}
	switch s.State {
	case portal.Asking:
		d.State = ui.PortalAsking
	case portal.Bound:
		d.State = ui.PortalBound
	case portal.Refused:
		d.State = ui.PortalRefused
	default:
		d.State = ui.PortalFailed
	}
	if s.Err != nil {
		d.Problem = s.Err.Error()
	}
	return d
}
