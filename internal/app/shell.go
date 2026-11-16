package app

import (
	"net/url"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/driver/desktop"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/history"
	"github.com/opragmatico/goscriber/internal/hotkey"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/pipeline"
	"github.com/opragmatico/goscriber/internal/platform"
	"github.com/opragmatico/goscriber/internal/session"
	"github.com/opragmatico/goscriber/internal/text"
	"github.com/opragmatico/goscriber/internal/typing"
	"github.com/opragmatico/goscriber/internal/ui"
)

// newHotkeyBackend registers global shortcuts with the OS,
// accessibilityTrusted reports the macOS Accessibility permission, and
// listDevices lists the audio devices for the settings window (nil: the
// system's); tests replace them.
var (
	newHotkeyBackend     = hotkey.SystemBackend
	currentSession       = session.Current
	accessibilityTrusted = platform.AccessibilityTrusted
	listDevices          func() (inputs, outputs []audio.Device, err error)
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
	// Onboarding opens the window on onboarding, where the user left it,
	// in place of the settings pages.
	Onboarding bool
}

// planLaunch follows Handy: the tray is shown when the desktop supports one,
// the show_tray_icon setting allows it and --no-tray is absent. With a tray
// the app opens no window until one is asked for (so start_hidden has nothing
// to hide yet). Without one the settings window is the only way in, so it
// opens even when starting hidden, and macOS keeps the Dock icon.
//
// Onboarding that is not finished opens at launch, unless the app starts
// hidden with a tray: the tray's "No model: Download…" leads back to it.
func planLaunch(s config.Settings, opts Options, traySupported, onboarding bool) launch {
	tray := traySupported && s.ShowTrayIcon && !opts.NoTray
	hidden := s.StartHidden || opts.StartHidden
	return launch{
		Tray:       tray,
		Window:     !tray,
		Hidden:     hidden,
		Onboarding: onboarding && (!tray || !hidden),
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
	history   *history.Store // nil when it could not be opened
	dictation *pipeline.Coordinator
	overlay   *ui.Overlay
	// overlayActive is whether a session is in progress, for the overlay's
	// clock; it is read off the main thread.
	overlayActive atomic.Bool
	// recording is what the shortcuts were last told about dictation, and
	// liveSession the session the overlay last showed recording, whose
	// interim text it shows; both main thread only.
	recording           bool
	liveSession         uint64
	stopDictationEvents func()
	hotkeys             *hotkey.Manager
	tray                *ui.Tray // nil without a tray
	settings            *ui.SettingsWindow
	desk                desktop.App

	// secureInput reports macOS Secure Event Input; nil skips the check.
	secureInput func() bool
	// session is the kind of desktop session. On Wayland the desktop owns
	// the shortcuts: through its global shortcuts portal (shortcuts.go),
	// or as a shortcut of its own that toggles dictation through SIGUSR2.
	session session.Kind
	// portal is the desktop's global shortcuts portal, once it holds the
	// shortcuts; portalClosed is set as the app stops.
	portalMu     sync.Mutex
	portal       desktopShortcuts
	portalClosed bool
	// notices collects background problems and routes them to the user.
	notices *notice.Hub
	// trusted reports the macOS Accessibility permission.
	trusted      func() bool
	stopNotices  func()
	hasModel     bool // the last settings named a model
	modelChecked bool // followModel has run
	// onboardingStep is where onboarding resumes at launch.
	onboardingStep ui.OnboardingStep
	// askedPermissions asks for the permissions once per launch: at launch,
	// or once onboarding, whose permissions step asks instead, is over.
	askedPermissions sync.Once

	unsubscribe func()
	// stopControl ends serving the control socket; nil without one.
	stopControl func()
	following   sync.WaitGroup
	quit        chan struct{} // closed when the app stops
	quitOnce    sync.Once
	stopOnce    sync.Once
	// permissionRows updates the settings window's Permissions page.
	permissionRows *latest
}

func newShell(rt *Runtime, a fyne.App, opts Options) *shell {
	desk, traySupported := a.(desktop.App)
	step, onboarding := pendingOnboarding(a.Preferences(), rt.Settings.Get())
	sh := &shell{
		rt:             rt,
		app:            a,
		opts:           opts,
		plan:           planLaunch(rt.Settings.Get(), opts, traySupported, onboarding),
		onboardingStep: step,
		do:             fyne.Do,
		desk:           desk,
		quit:           make(chan struct{}),
		notices:        notice.NewHub(notice.Options{Log: rt.Log.Logger}),
		trusted:        accessibilityTrusted,
		session:        currentSession(),
	}
	sh.svc = newServices(rt, opts, sh.notices)
	sh.history = openHistory(rt)
	sh.permissionRows = &latest{do: func(f func()) { sh.do(f) }}
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

	sh.settings = sh.newSettingsWindow()
	sh.refreshPermissions()

	if sh.plan.Tray {
		sh.tray = ui.NewTray(sh.desk, ui.TrayOptions{
			Actions: ui.TrayActions{
				StartDictation: func() { sh.dictate(pipeline.ActionStart) },
				StopDictation:  func() { sh.dictate(pipeline.ActionStop) },
				Cancel:         func() { sh.dictate(pipeline.ActionCancel) },
				OpenModels:     sh.openModels,
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
	sh.hotkeys = hotkey.NewManager(sh.hotkeyBackend(), hotkey.Options{
		OnAction:     func(a hotkey.Action) { sh.dictate(dictationAction(a)) },
		OnError:      sh.shortcutFailed,
		OnRegistered: sh.shortcutRegistered,
		Log:          sh.rt.Log.Logger,
	})
	sh.hotkeys.Apply(hotkey.ConfigFrom(s))
	sh.startDictation()
	// Later launches and SIGUSR2 reach dictation from here on.
	sh.serveControl()
	sh.watchToggleSignal()

	sh.app.Lifecycle().SetOnStarted(sh.started)
	// Stop following settings as soon as Fyne shuts down; stop then waits for
	// the follower to finish.
	sh.app.Lifecycle().SetOnStopped(sh.stopFollowing)
	sh.follow()

	sh.rt.Log.Info("app shell ready",
		"tray", sh.plan.Tray,
		"window_at_launch", sh.plan.Window,
		"start_hidden", sh.plan.Hidden,
		"onboarding", sh.plan.Onboarding,
		"model", model,
		"shortcut", s.TranscribeShortcut,
		"activation", string(s.ActivationMode))
}

// started runs on the main thread once Fyne is up. Onboarding asks for the
// permissions on its own step, so the launch leaves them to it.
func (sh *shell) started() {
	platform.SetDockIcon(!sh.plan.Tray)
	switch {
	case sh.plan.Onboarding:
		sh.rt.Log.Info("opening onboarding", "step", sh.onboardingStep.String())
		sh.settings.ShowOnboarding(sh.onboardingStep)
	case sh.plan.Window:
		sh.settings.Show(ui.PageGeneral)
	}
	if sh.secureInput != nil {
		sh.following.Go(sh.watchSecureInput)
	}
	if runtime.GOOS == "darwin" {
		if !sh.plan.Onboarding {
			sh.askPermissionsOnce()
		}
		sh.following.Go(sh.watchPermissions)
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

// newSettingsWindow prepares the settings window over the settings store,
// the model files, the log and the shortcuts.
func (sh *shell) newSettingsWindow() *ui.SettingsWindow {
	engineVersion, engineGPU := "", ""
	if sh.opts.EngineVersion != nil {
		engineVersion = sh.opts.EngineVersion()
	}
	if sh.opts.EngineGPU != nil {
		engineGPU = sh.opts.EngineGPU()
	}
	var tools []config.TypingTool
	if runtime.GOOS == "linux" {
		tools = typing.Installed(exec.LookPath, config.TypingXdotool, config.TypingWtype, config.TypingDotool, config.TypingYdotool)
	}
	return ui.NewSettingsWindow(sh.app, ui.SettingsOptions{
		Info: ui.SettingsInfo{
			Version:       sh.opts.Version,
			EngineVersion: engineVersion,
			EngineGPU:     engineGPU,
			SettingsFile:  sh.rt.Settings.Path(),
			LogsDir:       sh.rt.Paths.LogsDir(),
			Session:       sh.session,
			TypingTools:   tools,
		},
		Store: sh.rt.Settings,
		// The language-independent fillers and English's: the list most
		// transcripts get.
		DefaultFillerWords: text.Fillers("en"),
		Models:             sh.svc.files,
		History:            sh.historyStore(),
		HistoryActions:     sh.historyActions(),
		Devices:            listDevices,
		Logs:               sh.rt.Log.Ring().Entries,
		ForceDebug:         sh.opts.Debug,
		OnClose:            sh.settingsClosed,
		// While a shortcut field listens, the global shortcuts would take
		// the very keys the user presses.
		OnRecordShortcut:       func(on bool) { sh.hotkeys.Suspend(on) },
		ChangeDesktopShortcuts: sh.changeDesktopShortcuts,
		OpenFolder:             func(dir string) { sh.openURL((&url.URL{Scheme: "file", Path: dir}).String()) },
		OnOnboardingStep:       sh.onboardingStepShown,
		OnOnboardingDone:       sh.onboardingDone,
		Do:                     func(f func()) { sh.do(f) },
		Log:                    sh.rt.Log.Logger,
	})
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
		if sh.stopControl != nil {
			sh.stopControl()
		}
		if sh.stopNotices != nil {
			sh.stopNotices()
		}
		if sh.stopDictationEvents != nil {
			sh.stopDictationEvents()
		}
		if sh.hotkeys != nil {
			sh.hotkeys.Close() // returns at once: on macOS the main loop may be gone
		}
		sh.closeShortcutPortal()
	})
}

// applySettings updates the UI after a settings change. A change to
// show_tray_icon takes effect at the next launch: Fyne cannot remove a tray
// icon once it is installed.
func (sh *shell) applySettings(s config.Settings) {
	model := modelName(s)
	sh.settings.SetSettings(s)
	if sh.tray != nil {
		sh.tray.SetModel(model)
		sh.tray.SetDebug(sh.debugMode(s))
	}
	sh.hotkeys.Apply(hotkey.ConfigFrom(s))
	sh.applyDictationSettings(s)
	sh.followHistory(s)
	sh.followModel(s)
}

func (sh *shell) debugMode(s config.Settings) bool { return s.DebugMode || sh.opts.Debug }

// modelName is the display name of the model dictation would use, or "" when
// there is none. Until the models manager lands (issue #13) it trusts the
// selected_model setting; the manager will also check that the file is on
// disk and supply the catalog's display name.
func modelName(s config.Settings) string { return s.SelectedModel }
