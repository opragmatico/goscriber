package app

import (
	"log/slog"
	"sync"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/driver/desktop"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/platform"
	"github.com/opragmatico/goscriber/internal/ui"
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

	dictation dictation
	tray      *ui.Tray // nil without a tray
	settings  *ui.SettingsWindow
	desk      desktop.App

	unsubscribe func()
	following   sync.WaitGroup
}

func newShell(rt *Runtime, a fyne.App, opts Options) *shell {
	desk, traySupported := a.(desktop.App)
	return &shell{
		rt:        rt,
		app:       a,
		opts:      opts,
		plan:      planLaunch(rt.Settings.Get(), opts, traySupported),
		do:        fyne.Do,
		dictation: pendingDictation{log: rt.Log.Logger},
		desk:      desk,
	}
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
				StartDictation: sh.dictation.Start,
				StopDictation:  sh.dictation.Stop,
				Cancel:         sh.dictation.Cancel,
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

	sh.app.Lifecycle().SetOnStarted(sh.started)
	// Stop following settings as soon as Fyne shuts down; stop then waits for
	// the follower to finish.
	sh.app.Lifecycle().SetOnStopped(sh.stopFollowing)
	sh.follow()

	sh.rt.Log.Info("app shell ready",
		"tray", sh.plan.Tray,
		"window_at_launch", sh.plan.Window,
		"start_hidden", sh.plan.Hidden,
		"model", model)
}

// started runs on the main thread once Fyne is up.
func (sh *shell) started() {
	platform.SetDockIcon(!sh.plan.Tray)
	if sh.plan.Window {
		sh.settings.Show(ui.PageGeneral)
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

// stop waits for the settings follower; call it after the app has run.
func (sh *shell) stop() {
	sh.stopFollowing()
	sh.following.Wait()
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

func (sh *shell) stopFollowing() {
	if sh.unsubscribe != nil {
		sh.unsubscribe()
	}
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
}

func (sh *shell) debugMode(s config.Settings) bool { return s.DebugMode || sh.opts.Debug }

// modelName is the display name of the model dictation would use, or "" when
// there is none. Until the models manager lands (issue #13) it trusts the
// selected_model setting; the manager will also check that the file is on
// disk and supply the catalog's display name.
func modelName(s config.Settings) string { return s.SelectedModel }

// dictation is what the tray's dictation items drive. The coordinator (issue
// #20) will implement it.
type dictation interface {
	Start()
	Stop()
	Cancel()
}

// pendingDictation stands in for the coordinator and only logs the request.
type pendingDictation struct{ log *slog.Logger }

func (d pendingDictation) Start()  { d.note("start") }
func (d pendingDictation) Stop()   { d.note("stop") }
func (d pendingDictation) Cancel() { d.note("cancel") }

func (d pendingDictation) note(action string) {
	d.log.Warn("dictation is not implemented yet", "action", action)
}
