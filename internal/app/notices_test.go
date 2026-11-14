package app

import (
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/hotkey"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/platform"
	"github.com/opragmatico/goscriber/internal/ui"
)

func newNoticeShell(t *testing.T, settings string) (*shell, *trayApp, uiQueue) {
	t.Helper()
	testShortcuts.reset()
	return newNoticeShellKeepingShortcuts(t, settings)
}

// defaultTalk is how the shortcut backend names the default transcribe
// shortcut, which differs by OS: "option+space" on macOS, "ctrl+alt+space"
// on Linux, "ctrl+space" on Windows.
func defaultTalk(t *testing.T) string {
	t.Helper()
	b, err := hotkey.Parse(config.Defaults().TranscribeShortcut)
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func newNoticeShellKeepingShortcuts(t *testing.T, settings string) (*shell, *trayApp, uiQueue) {
	t.Helper()
	home := isolate(t)
	if settings != "" {
		writeSettings(t, home, settings)
	}
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	return sh, a, q
}

// With no model downloaded, the user hears about it at launch as an OS
// notification (no window is open), the tray turns amber, and the notice
// ends once a model is selected.
func TestNoModelNotice(t *testing.T) {
	sh, a, q := newNoticeShell(t, "")
	q.runNext(t)
	if len(a.notifications) != 1 || a.notifications[0].Title != "No model downloaded" {
		t.Fatalf("notifications %+v", a.notifications)
	}
	if a.trayIcon != ui.TrayAttentionIcon() {
		t.Error("the tray should show the attention icon")
	}
	if alert := sh.tray.Alert(); alert == nil || alert.Label != "" {
		t.Errorf("alert %+v: the menu already offers the download, so no extra item", alert)
	}
	b := sh.settings.Banner()
	if b == nil || b.ActionLabel != "Download a model" {
		t.Fatalf("banner %+v", b)
	}
	b.OnAction()
	if !sh.settings.Visible() || sh.settings.Page() != ui.PageModels {
		t.Error("the banner's button should open the Models page")
	}

	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.SelectedModel = "parakeet-tdt-0.6b-v2" }); err != nil {
		t.Fatal(err)
	}
	q.runNext(t)
	if sh.settings.Banner() != nil || sh.tray.Alert() != nil || a.trayIcon != ui.TrayIcon(ui.TrayIdle) {
		t.Error("selecting a model should clear the notice everywhere")
	}

	// The model going away again is news; other settings changes are not.
	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.SelectedModel = "" }); err != nil {
		t.Fatal(err)
	}
	q.runNext(t)
	if len(a.notifications) != 1 {
		t.Errorf("%d notifications: the window is open, so the banner shows it instead", len(a.notifications))
	}
	if sh.settings.Banner() == nil {
		t.Fatal("no banner for the missing model")
	}
	sh.settings.Banner().OnDismiss()
	q.runNext(t)
	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.HoldThresholdMS = 400 }); err != nil {
		t.Fatal(err)
	}
	q.runNext(t)
	if sh.settings.Banner() != nil {
		t.Error("a dismissed notice came back after an unrelated settings change")
	}
}

// A shortcut that cannot register because Accessibility is missing raises
// the permission notice, with a tray item that opens System Settings; once
// the permission is granted, the shortcut registers again and the notice
// ends.
func TestAccessibilityNotice(t *testing.T) {
	poll := permissionsPoll
	permissionsPoll = 5 * time.Millisecond
	t.Cleanup(func() { permissionsPoll = poll })

	var mu sync.Mutex
	trusted := false
	prev := accessibilityTrusted
	accessibilityTrusted = func() bool { mu.Lock(); defer mu.Unlock(); return trusted }
	t.Cleanup(func() { accessibilityTrusted = prev })

	// The event tap refuses to register without the permission.
	testShortcuts.reset()
	testShortcuts.setFail(errors.New("grant Accessibility"))
	sh, a, q := newNoticeShellKeepingShortcuts(t, `{"schema_version": 1, "selected_model": "m"}`)
	talk := defaultTalk(t)
	testShortcuts.waitLog(t, "!"+talk)
	q.runNext(t)
	alert := sh.tray.Alert()
	if alert == nil || alert.Label != "⚠ Permission needed…" {
		t.Fatalf("alert %+v", alert)
	}
	alert.Action()
	if len(a.opened) != 1 || a.opened[0] != platform.PermAccessibility.SettingsURL() {
		t.Errorf("opened %v, want System Settings at Accessibility", a.opened)
	}

	// started watches the permission on macOS only, the one OS that has it.
	// The watcher works the same everywhere, so elsewhere start it directly.
	if runtime.GOOS == "darwin" {
		sh.started()
	} else {
		sh.following.Go(sh.watchPermissions)
	}
	testShortcuts.setFail(nil)
	mu.Lock()
	trusted = true
	mu.Unlock()
	testShortcuts.waitLog(t, "!"+talk, "+"+talk)
	// The watcher keeps posting Permissions page refreshes, so runNext would
	// never see the queue go quiet: run updates until the notice ends.
	q.waitUI(t, "granting the permission to end the notice", func() bool {
		return sh.tray.Alert() == nil && sh.notices.Count(notice.CodePermissionMissing) == 0
	})
}

// With the permission in place, a shortcut that cannot register is the
// shortcut's problem, and its next registration clears it.
func TestShortcutNotice(t *testing.T) {
	sh, a, q := newNoticeShell(t, `{"schema_version": 1, "selected_model": "m"}`)
	sh.trusted = func() bool { return true }
	testShortcuts.waitLog(t, "+"+defaultTalk(t))

	sh.shortcutFailed(&hotkey.RegisterError{Purpose: hotkey.PurposeTranscribe, Shortcut: "f5", Err: errors.New("taken")})
	q.runNext(t)
	if len(a.notifications) != 1 || a.notifications[0].Title != "Shortcut unavailable" {
		t.Fatalf("notifications %+v", a.notifications)
	}
	if sh.tray.Alert() != nil {
		t.Error("a transient problem should not put an alert in the tray")
	}
	sh.shortcutRegistered(hotkey.PurposeTranscribe)
	q.runNext(t)
	if sh.notices.Count(notice.CodeShortcutFailed) != 0 || sh.settings.Banner() != nil {
		t.Error("registering the shortcut should clear its notice")
	}
}
