package app

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/driver/desktop"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/hotkey"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/pipeline"
	"github.com/opragmatico/goscriber/internal/platform"
	"github.com/opragmatico/goscriber/internal/ui"
)

// newHotkeyBackend registers global shortcuts with the OS, and
// accessibilityTrusted reports the macOS Accessibility permission; tests
// replace them.
var (
	newHotkeyBackend     = hotkey.SystemBackend
	accessibilityTrusted = platform.AccessibilityTrusted
)

// How often the shell checks for macOS Secure Event Input, and how long it
// must stay on before a warning; tests shorten them.
var (
	secureInputPoll    = time.Second
	secureInputSustain = hotkey.DefaultSecureInputSustain
)

// ID is Goscriber's application ID. Fyne keys its storage by it, and the
// macOS bundle uses it as CFBundleIdentifier.
const ID = "io.github.opragmatico.goscriber"

// Run starts the desktop app and blocks until it quits, from the tray's Quit
// item or on SIGINT / SIGTERM, which Fyne turns into a quit. The runtime is
// closed on the way out.
func Run(opts Options) error {
	return run(opts, func() fyne.App { return fyneapp.NewWithID(ID) })
}

func run(opts Options, newApp func() fyne.App) error {
	rt, err := Start(opts)
	if err != nil {
		return err
	}
	defer rt.Close()

	sh := newShell(rt, newApp(), opts)
	sh.start()
	sh.app.Run()
	sh.stop()
	rt.Log.Info("goscriber stopped")
	return nil
}

// launch is what the shell puts on screen at startup.
type launch struct {
	Tray   bool // install the tray icon and menu
	Window bool // open the settings window at launch
	Hidden bool // start_hidden or --start-hidden asked for no window at launch
}

// planLaunch follows Handy: the tray is shown when the desktop supports one,
// the show_tray_icon setting allows it and --no-tray is absent. With a tray
// the app opens no window until one is asked for (so start_hidden has nothing
// to hide yet). Without one the settings window is the only way in, so it
// opens even when starting hidden, and macOS keeps the Dock icon.
func planLaunch(s config.Settings, opts Options, traySupported bool) launch {
	tray := traySupported && s.ShowTrayIcon && !opts.NoTray
	return launch{
		Tray:   tray,
		Window: !tray,
		Hidden: s.StartHidden || opts.StartHidden,
	}
}

// shell is the running desktop app: the tray, the settings window and the
// services behind them.
type shell struct {
	rt   *Runtime
	app  fyne.App
	opts Options
	plan launch
	// do runs UI updates from other goroutines on the Fyne main thread.
	do func(func())

	svc       services
	dictation *pipeline.Coordinator
	overlay   *ui.Overlay
	// overlayActive is whether a session is in progress, for the overlay's
	// clock; it is read off the main thread.
	overlayActive atomic.Bool
	// recording is what the shortcuts were last told about dictation.
	recording           bool
	stopDictationEvents func()
	hotkeys             *hotkey.Manager
	tray                *ui.Tray // nil without a tray
	settings            *ui.SettingsWindow
	desk                desktop.App

	// secureInput reports macOS Secure Event Input; nil skips the check.
	secureInput func() bool
	// notices collects background problems and routes them to the user.
	notices *notice.Hub
	// trusted reports the macOS Accessibility permission.
	trusted      func() bool
	stopNotices  func()
	hasModel     bool // the last settings named a model
	modelChecked bool // followModel has run

	unsubscribe func()
	following   sync.WaitGroup
	quit        chan struct{} // closed when the app stops
	quitOnce    sync.Once
	stopOnce    sync.Once
}

func newShell(rt *Runtime, a fyne.App, opts Options) *shell {
	desk, traySupported := a.(desktop.App)
	sh := &shell{
		rt:      rt,
		app:     a,
		opts:    opts,
		plan:    planLaunch(rt.Settings.Get(), opts, traySupported),
		do:      fyne.Do,
		desk:    desk,
		quit:    make(chan struct{}),
		notices: notice.NewHub(notice.Options{Log: rt.Log.Logger}),
		trusted: accessibilityTrusted,
	}
	sh.svc = newServices(rt, opts, sh.notices)
	if runtime.GOOS == "darwin" {
		sh.secureInput = platform.SecureInputEnabled
	}
	return sh
}

// start builds the UI from the current settings and follows later changes.
// Call it on the main goroutine before the app runs.
func (sh *shell) start() {
	s := sh.rt.Settings.Get()
	model := modelName(s)

	sh.app.SetIcon(ui.AppIcon())

	sh.settings = ui.NewSettingsWindow(sh.app, ui.SettingsInfo{
		Version:      sh.opts.Version,
		SettingsFile: sh.rt.Settings.Path(),
		LogsDir:      sh.rt.Paths.LogsDir(),
	}, sh.settingsClosed)
	sh.settings.SetModel(model)

	if sh.plan.Tray {
		sh.tray = ui.NewTray(sh.desk, ui.TrayOptions{
			Actions: ui.TrayActions{
				StartDictation: func() { sh.dictate(pipeline.ActionStart) },
				StopDictation:  func() { sh.dictate(pipeline.ActionStop) },
				Cancel:         func() { sh.dictate(pipeline.ActionCancel) },
				OpenModels:     func() { sh.settings.Show(ui.PageModels) },
				OpenSettings:   func() { sh.settings.Show(ui.PageGeneral) },
				Quit:           sh.app.Quit,
			},
			Model: model,
			Debug: sh.debugMode(s),
		})
	} else if sh.desk == nil && s.ShowTrayIcon && !sh.opts.NoTray {
		sh.rt.Log.Warn("no system tray on this desktop; opening the settings window instead")
	}

	sh.routeNotices()
	sh.followModel(s)

	// The shortcuts register in the background: on macOS that waits for
	// Fyne's main loop, which starts with the app. Their actions go
	// straight to the coordinator, which never blocks.
	sh.hotkeys = hotkey.NewManager(newHotkeyBackend(), hotkey.Options{
		OnAction:     func(a hotkey.Action) { sh.dictate(dictationAction(a)) },
		OnError:      sh.shortcutFailed,
		OnRegistered: sh.shortcutRegistered,
		Log:          sh.rt.Log.Logger,
	})
	sh.hotkeys.Apply(hotkey.ConfigFrom(s))
	sh.startDictation()

	sh.app.Lifecycle().SetOnStarted(sh.started)
	// Stop following settings as soon as Fyne shuts down; stop then waits for
	// the follower to finish.
	sh.app.Lifecycle().SetOnStopped(sh.stopFollowing)
	sh.follow()

	sh.rt.Log.Info("app shell ready",
		"tray", sh.plan.Tray,
		"window_at_launch", sh.plan.Window,
		"start_hidden", sh.plan.Hidden,
		"model", model,
		"shortcut", s.TranscribeShortcut,
		"activation", string(s.ActivationMode))
}

// started runs on the main thread once Fyne is up.
func (sh *shell) started() {
	platform.SetDockIcon(!sh.plan.Tray)
	if sh.plan.Window {
		sh.settings.Show(ui.PageGeneral)
	}
	if sh.secureInput != nil {
		sh.following.Go(sh.watchSecureInput)
	}
	if runtime.GOOS == "darwin" {
		sh.following.Go(sh.watchAccessibility)
	}
	sh.following.Go(sh.openMicrophone)
}

// dictationAction is the coordinator's action for a shortcut's.
func dictationAction(a hotkey.Action) pipeline.Action {
	switch a {
	case hotkey.ActionStart:
		return pipeline.ActionStart
	case hotkey.ActionStop:
		return pipeline.ActionStop
	case hotkey.ActionCancel:
		return pipeline.ActionCancel
	default:
		return 0
	}
}

// watchSecureInput warns while macOS Secure Event Input keeps the shortcut
// from being heard, until the app stops.
func (sh *shell) watchSecureInput() {
	w := hotkey.SecureInputWatch{Sustain: secureInputSustain}
	tick := time.NewTicker(secureInputPoll)
	defer tick.Stop()
	for {
		select {
		case <-sh.quit:
			return
		case now := <-tick.C:
			switch w.Observe(sh.secureInput(), now) {
			case hotkey.SecureInputBlocking:
				args := []any{}
				if pid, name, ok := platform.SecureInputHolder(); ok {
					args = append(args, "held_by", name, "pid", pid)
				}
				sh.rt.Log.Warn("Secure Event Input is on, so the shortcut cannot be heard until it is off; "+
					"close the password prompt or turn off Secure Keyboard Entry in Terminal", args...)
			case hotkey.SecureInputCleared:
				sh.rt.Log.Info("Secure Event Input is off; the shortcut works again")
			}
		}
	}
}

// settingsClosed runs after the user closes the settings window, which hides
// it. Without a tray the window was the only way into the app, so closing it
// quits.
func (sh *shell) settingsClosed() {
	if !sh.plan.Tray {
		sh.app.Quit()
	}
}

// stop waits for the settings follower and ends dictation; call it after
// the app has run.
func (sh *shell) stop() {
	sh.stopFollowing()
	sh.following.Wait()
	sh.stopOnce.Do(sh.stopDictation)
}

func (sh *shell) follow() {
	updates, unsubscribe := sh.rt.Settings.Subscribe()
	sh.unsubscribe = unsubscribe
	sh.following.Go(func() {
		for s := range updates {
			sh.do(func() { sh.applySettings(s) })
		}
	})
}

// stopFollowing stops following settings, releases the shortcuts and ends
// the background watchers. It runs when Fyne stops, and again from stop.
func (sh *shell) stopFollowing() {
	if sh.unsubscribe != nil {
		sh.unsubscribe()
	}
	sh.quitOnce.Do(func() {
		close(sh.quit)
		if sh.stopNotices != nil {
			sh.stopNotices()
		}
		if sh.stopDictationEvents != nil {
			sh.stopDictationEvents()
		}
		if sh.hotkeys != nil {
			sh.hotkeys.Close() // returns at once: on macOS the main loop may be gone
		}
	})
}

// applySettings updates the UI after a settings change. A change to
// show_tray_icon takes effect at the next launch: Fyne cannot remove a tray
// icon once it is installed.
func (sh *shell) applySettings(s config.Settings) {
	model := modelName(s)
	sh.settings.SetModel(model)
	if sh.tray != nil {
		sh.tray.SetModel(model)
		sh.tray.SetDebug(sh.debugMode(s))
	}
	sh.hotkeys.Apply(hotkey.ConfigFrom(s))
	sh.applyDictationSettings(s)
	sh.followModel(s)
}

func (sh *shell) debugMode(s config.Settings) bool { return s.DebugMode || sh.opts.Debug }

// modelName is the display name of the model dictation would use, or "" when
// there is none. Until the models manager lands (issue #13) it trusts the
// selected_model setting; the manager will also check that the file is on
// disk and supply the catalog's display name.
func modelName(s config.Settings) string { return s.SelectedModel }
