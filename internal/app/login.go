package app

import (
	"errors"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/platform"
)

// The OS's login item for Goscriber, and whether macOS opened the app as
// one; tests replace them, so no test touches the real login item.
var (
	loginItemSupported = platform.LoginItemSupported
	loginItemEnabled   = platform.LoginItemEnabled
	setLoginItem       = platform.SetLoginItem
	launchedAtLogin    = platform.LaunchedAtLogin
)

// startLoginItem keeps the login item and the autostart setting in step
// from launch until the app stops. It runs from start, once the settings
// are followed, so the settings window sees the setting change.
//
// At launch the login item is the truth: the user may have removed it in
// System Settings, or the desktop's startup applications, since the last
// run, so the setting takes its state. A login item that is on is written
// again, so that it starts this executable wherever it is now (Linux and
// Windows keep its path). Reading and writing it are quick, so this
// happens before the app runs. After that, each change of the setting adds
// or removes the login item, on a goroutine (followLoginItem).
//
// Where there can be no login item (a bare executable on macOS) the
// setting is left alone, and the settings window says why.
func (sh *shell) startLoginItem() {
	on, err := loginItemEnabled()
	if err != nil {
		sh.rt.Log.Info("launching at login is not available", "reason", err)
		return
	}
	if on {
		if err := setLoginItem(true); err != nil {
			sh.rt.Log.Warn("could not update the login item", "err", err)
		}
	}
	sh.matchAutostart(on)
	updates, unsubscribe := sh.rt.Settings.Subscribe()
	sh.following.Go(func() {
		defer unsubscribe()
		sh.followLoginItem(on, updates)
	})
}

// followLoginItem adds or removes the login item, which is on or not, as
// the autostart setting changes, until the app stops. If that fails, a
// notice says why and the setting goes back to what the OS has.
func (sh *shell) followLoginItem(on bool, updates <-chan config.Settings) {
	for {
		select {
		case <-sh.quit:
			return
		case s, ok := <-updates:
			if !ok {
				return
			}
			if s.Autostart != on {
				on = sh.applyAutostart(s.Autostart)
			}
		}
	}
}

// applyAutostart adds the login item, or removes it, and returns whether it
// is on now. When the OS refuses, a notice says why, and the setting goes
// back to what the OS has, unless it was changed again meanwhile.
func (sh *shell) applyAutostart(want bool) bool {
	err := setLoginItem(want)
	switch {
	case err == nil:
		sh.rt.Log.Info("launch at login turned "+onOff(want), "autostart", want)
		return want
	case errors.Is(err, platform.ErrLoginItemNeedsApproval):
		sh.rt.Log.Warn("the login item waits for approval in System Settings")
		sh.notices.Publish(notice.LoginItemNeedsApproval())
	default:
		sh.rt.Log.Warn("could not turn launch at login "+onOff(want), "err", err)
		sh.notices.Publish(notice.LoginItemFailed(want, err))
	}
	on, err := loginItemEnabled()
	if err != nil {
		on = !want
	}
	if on != want {
		err := sh.rt.Settings.Update(func(s *config.Settings) {
			if s.Autostart == want {
				s.Autostart = on
			}
		})
		if err != nil {
			sh.rt.Log.Warn("could not save the autostart setting", "err", err)
		}
	}
	return on
}

// matchAutostart saves on as the autostart setting at launch, unless it
// says so already.
func (sh *shell) matchAutostart(on bool) {
	if sh.rt.Settings.Get().Autostart == on {
		return
	}
	sh.rt.Log.Info("the autostart setting follows the login item", "autostart", on)
	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.Autostart = on }); err != nil {
		sh.rt.Log.Warn("could not save the autostart setting", "err", err)
	}
}

// loginItemUnavailable says why there can be no login item, for the
// settings window, or is "" when there can be one.
func loginItemUnavailable() string {
	if err := loginItemSupported(); err != nil {
		return err.Error()
	}
	return ""
}

// noteLaunchedAtLogin runs when the app has started, on the main thread.
// macOS tells a launch at login only then, by the Apple Event that opened
// the app; it then starts hidden, as --at-login does on Linux and Windows:
// unfinished onboarding waits behind the tray, which leads back to it, and
// the missing model is the notice it would have been.
func (sh *shell) noteLaunchedAtLogin() {
	if sh.opts.AtLogin || !launchedAtLogin() {
		return
	}
	opts := sh.opts
	opts.AtLogin = true
	onboarding := sh.plan.Onboarding
	sh.plan = planLaunch(sh.rt.Settings.Get(), opts, sh.desk != nil, onboarding)
	sh.rt.Log.Info("launched at login", "start_hidden", sh.plan.Hidden, "onboarding", sh.plan.Onboarding)
	if onboarding && !sh.plan.Onboarding {
		sh.modelChecked = false
		sh.followModel(sh.rt.Settings.Get())
	}
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
