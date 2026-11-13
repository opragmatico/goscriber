package app

import (
	"time"

	"fyne.io/fyne/v2"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/hotkey"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/platform"
	"github.com/opragmatico/goscriber/internal/ui"
)

// permissionsPoll is how often the shell checks whether a missing
// permission has been granted; tests shorten it.
var permissionsPoll = 2 * time.Second

// routeNotices shows every notice where the user will see it, once: an OS
// notification when no window is open, a banner in the settings window
// (also when it opens later, while the notice lasts), and the tray's amber
// icon for persistent problems such as a missing permission or model.
func (sh *shell) routeNotices() {
	events, stop := sh.notices.Subscribe()
	sh.stopNotices = stop
	sh.following.Go(func() {
		for ev := range events {
			sh.do(func() { sh.showNotice(ev) })
		}
	})
}

// showNotice runs on the main thread.
func (sh *shell) showNotice(ev notice.Event) {
	if !ev.Ended && !sh.settings.Visible() {
		sh.app.SendNotification(fyne.NewNotification(ev.Notice.Title(), ev.Notice.Message))
	}
	sh.settings.SetBanner(sh.banner(ev.Active))
	if sh.tray != nil {
		sh.tray.SetAlert(sh.trayAlert(ev.Active))
	}
}

// banner shows the newest error, or else the newest warning.
func (sh *shell) banner(active []notice.Notice) *ui.Banner {
	if len(active) == 0 {
		return nil
	}
	n := active[0]
	for _, a := range active {
		if a.Level == notice.LevelError {
			n = a
			break
		}
	}
	return &ui.Banner{
		Error:       n.Level == notice.LevelError,
		Title:       n.Title(),
		Message:     n.Message,
		ActionLabel: n.Action.Label,
		OnAction:    func() { sh.noticeAction(n.Action.ID) },
		OnDismiss:   func() { sh.notices.Resolve(n.Code) },
	}
}

// trayAlert shows the newest persistent problem. A missing model needs no
// menu item of its own: the menu already offers the download.
func (sh *shell) trayAlert(active []notice.Notice) *ui.TrayAlert {
	for _, n := range active {
		if !n.Persistent() {
			continue
		}
		alert := &ui.TrayAlert{}
		if n.Code != notice.CodeNoModel {
			alert.Label = "⚠ " + n.Title() + "…"
			id := n.Action.ID
			alert.Action = func() { sh.noticeAction(id) }
		}
		return alert
	}
	return nil
}

// noticeAction carries out a notice's button, on the main thread.
func (sh *shell) noticeAction(id notice.ActionID) {
	switch id {
	case notice.ActionOpenModels:
		sh.settings.Show(ui.PageModels)
	case notice.ActionOpenAccessibility:
		sh.openURL(platform.PermAccessibility.SettingsURL())
	case notice.ActionOpenMicrophone:
		sh.openURL(platform.PermMicrophone.SettingsURL())
	default:
		sh.settings.Show(ui.PageGeneral)
	}
}

// followModel reports a missing model at startup and whenever the model goes
// away, and clears the notice once there is one. A dismissed notice stays
// dismissed while nothing changes. It runs on the main thread.
func (sh *shell) followModel(s config.Settings) {
	has := modelName(s) != ""
	switch {
	case has:
		sh.notices.Resolve(notice.CodeNoModel)
	case sh.hasModel || !sh.modelChecked: // at startup, or the model went away
		sh.notices.Publish(notice.NoModel())
	}
	sh.hasModel, sh.modelChecked = has, true
}

// shortcutFailed runs on the hotkey manager's goroutine.
func (sh *shell) shortcutFailed(err error) {
	sh.notices.Publish(notice.ForShortcutError(err, sh.trusted()))
}

// shortcutRegistered runs on the hotkey manager's goroutine.
func (sh *shell) shortcutRegistered(p hotkey.Purpose) {
	if p != hotkey.PurposeTranscribe {
		return
	}
	sh.notices.Resolve(notice.CodeShortcutFailed)
	sh.notices.Resolve(notice.CodePermissionMissing)
}

// watchPermissions follows the permissions until the app stops: see
// checkPermissions.
func (sh *shell) watchPermissions() {
	tick := time.NewTicker(permissionsPoll)
	defer tick.Stop()
	for {
		select {
		case <-sh.quit:
			return
		case <-tick.C:
			sh.checkPermissions()
		}
	}
}
