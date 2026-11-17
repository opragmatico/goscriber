package ui

import (
	"log/slog"
	"runtime"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/logging"
	"github.com/opragmatico/goscriber/internal/session"
)

// Page is a page of the settings window.
type Page int

// Settings pages, in sidebar order. The Debug page is in the sidebar only in
// debug mode.
const (
	PageGeneral Page = iota
	PageModels
	PageAdvanced
	PagePermissions
	PageHistory
	PageDebug
	PageAbout
	pageCount
)

var pageTitles = [pageCount]string{"General", "Models", "Advanced", "Permissions", "History", "Debug", "About"}

// String returns the page's title in the sidebar.
func (p Page) String() string {
	if p < 0 || p >= pageCount {
		return "Page(?)"
	}
	return pageTitles[p]
}

// SettingsTitle is the settings window's title.
const SettingsTitle = "Goscriber Settings"

// SettingsInfo is what the settings window says about this build, the
// desktop it runs on and where its files are.
type SettingsInfo struct {
	Version string
	// EngineVersion is the NeMo-Speech.cpp runtime's version, or "" for a
	// build without the speech engine.
	EngineVersion string
	// EngineGPU names the engine's GPU backend ("Metal", "Vulkan",
	// "CUDA"), or is "" when the engine runs on the CPU only. The
	// accelerator setting offers only what the engine has.
	EngineGPU    string
	SettingsFile string
	LogsDir      string
	// OS is the operating system, as runtime.GOOS names it; empty means
	// this one. The paste keys and typing tool settings are Linux's.
	OS string
	// Session is the kind of graphical session. On Wayland the desktop
	// owns the dictation shortcut: through its global shortcuts portal
	// (SetDesktopShortcuts), or as a shortcut of its own that runs
	// ToggleCommand.
	Session session.Kind
	// TypingTools are the typing tools installed, which the typing tool
	// setting names.
	TypingTools []config.TypingTool
	// LoginItemUnavailable says why Goscriber cannot launch at login, such
	// as from a bare executable on macOS; the setting is then disabled. ""
	// means it can.
	LoginItemUnavailable string
}

func (i SettingsInfo) os() string {
	if i.OS != "" {
		return i.OS
	}
	return runtime.GOOS
}

// ToggleCommand is the command a desktop keyboard shortcut runs to start
// and stop dictation: SIGUSR2 to the running app, which the engine workers
// ignore.
const ToggleCommand = "pkill -USR2 -x goscriber"

// SettingsOptions configure the settings window. Store is required.
type SettingsOptions struct {
	Info SettingsInfo
	// Store holds the settings. Every widget shows a field of it and saves
	// the user's changes to it at once.
	Store SettingsStore
	// Models lists, downloads and deletes the model files for the Models
	// page. Nil shows the catalog with nothing to download.
	Models ModelFiles
	// History lists and deletes past dictations for the History page. Nil
	// says they are not being saved.
	History HistoryStore
	// HistoryActions carry out the History page's buttons.
	HistoryActions HistoryActions
	// Devices lists the microphones and outputs for the General page. Nil
	// means the system's, from internal/audio.
	Devices func() (inputs, outputs []audio.Device, err error)
	// Logs returns the most recent log records, for the Debug page.
	Logs func() []logging.Entry
	// DefaultFillerWords is the built-in filler-word list, shown while
	// custom_filler_words is null, as the start of the user's own list.
	DefaultFillerWords []string
	// ForceDebug shows the Debug page whatever debug_mode says, as --debug
	// does.
	ForceDebug bool
	// OnClose runs after the user closes the window, which only hides it so
	// it can be shown again.
	OnClose func()
	// OnRecordShortcut runs with true when a shortcut field starts listening
	// for a key combination and with false when it stops. The app suspends
	// its global shortcuts meanwhile, so pressing the current one reaches
	// the window instead of starting dictation.
	OnRecordShortcut func(recording bool)
	// ChangeDesktopShortcuts opens the desktop's dialog to change the keys
	// its global shortcuts portal bound, on Wayland. Nil leaves the button
	// out.
	ChangeDesktopShortcuts func() error
	// OpenFolder shows a folder, such as the logs directory, in the file
	// manager. Nil leaves those buttons out.
	OpenFolder func(dir string)
	// OnOnboardingStep runs each time onboarding shows a step, so the app
	// can resume it there after a quit.
	OnOnboardingStep func(step OnboardingStep)
	// OnOnboardingDone runs when the user finishes onboarding, or skips the
	// rest of it.
	OnOnboardingDone func(skipped bool)
	// Do runs f on the Fyne main thread; nil means fyne.Do. Downloads, the
	// model list and the device list are read in the background and shown
	// through it.
	Do func(f func())
	// Log receives settings that could not be saved and failed downloads.
	// Nil means slog's default logger.
	Log *slog.Logger
}

// SettingsWindow is the settings window: a sidebar of pages whose widgets
// are bound to the settings store, so a change applies at once and
// persists, and a change made elsewhere (the settings file, the tray) shows
// up as soon as the app passes it on with SetSettings. The Fyne window is
// created on first Show, so nothing is on screen until the user asks for
// it. Its methods must be called on the Fyne main thread.
type SettingsWindow struct {
	app  fyne.App
	opts SettingsOptions

	cur      config.Settings // the settings shown
	bindings []*binding
	col      *labelColumn // the label column of the page being built
	created  bool         // the pages and their bindings exist
	loading  bool         // the widgets are being brought in line with cur

	win     fyne.Window
	tabs    *container.AppTabs
	items   [pageCount]*container.TabItem
	banner  *Banner
	visible bool

	permissions []PermissionRow
	permBox     *fyne.Container

	// desktop is what the desktop's global shortcuts portal bound, on
	// Wayland, which keysView shows on the General page.
	desktop  DesktopShortcuts
	keysView *desktopKeysView

	language       LanguageSetting
	detected       string
	languageBox    *fyne.Container
	languageSelect *widget.Select
	languageNote   *widget.Label
	detectedLabel  *widget.Label

	devices    deviceState
	models     modelsState
	logs       logsState
	history    historyState
	onboarding onboardingState

	// confirm asks before something that cannot be undone, such as
	// deleting a model, with action as the button that does it; tests
	// replace it.
	confirm func(title, message, action string, ok func())
}

// NewSettingsWindow prepares the window; nothing is created until Show.
func NewSettingsWindow(a fyne.App, opts SettingsOptions) *SettingsWindow {
	w := &SettingsWindow{app: a, opts: opts}
	w.cur = opts.Store.Get()
	w.models.init()
	w.confirm = w.showConfirm
	return w
}

// showConfirm asks the user to confirm something that cannot be undone, in
// a modal pop-up over the window, and runs ok if they do.
func (w *SettingsWindow) showConfirm(title, message, action string, ok func()) {
	var pop *widget.PopUp
	cancel := widget.NewButton("Cancel", func() { pop.Hide() })
	confirm := widget.NewButton(action, func() {
		pop.Hide()
		ok()
	})
	confirm.Importance = widget.DangerImportance
	body := container.NewVBox(
		widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		paragraph(message),
		container.NewHBox(layout.NewSpacer(), cancel, confirm),
	)
	pop = widget.NewModalPopUp(container.NewPadded(body), w.win.Canvas())
	pop.Resize(fyne.NewSize(420, pop.MinSize().Height))
	pop.Show()
}

func (w *SettingsWindow) do(f func()) {
	if w.opts.Do != nil {
		w.opts.Do(f)
		return
	}
	fyne.Do(f)
}

func (w *SettingsWindow) log() *slog.Logger {
	if w.opts.Log != nil {
		return w.opts.Log
	}
	return slog.Default()
}

// Show opens the window on page p and brings it to the front, leaving
// onboarding if it is on screen. The Debug page opens only in debug mode;
// otherwise Show opens General instead. Each time, the model files, audio
// devices and history are read again, since they may have changed while the
// window was closed.
func (w *SettingsWindow) Show(p Page) {
	if w.win == nil {
		w.create()
	}
	w.leaveOnboarding()
	w.selectPage(p)
	w.win.Show()
	w.win.RequestFocus()
	w.visible = true
	w.refreshModelFiles()
	w.refreshDevices()
	w.refreshHistory()
}

// Raise brings the window to the front as it is, on the page or onboarding
// step it shows. It does nothing while the window is hidden.
func (w *SettingsWindow) Raise() {
	if w.win == nil || !w.visible {
		return
	}
	w.win.Show()
	w.win.RequestFocus()
}

// Hide takes the window off screen, as closing it does, without running
// OnClose.
func (w *SettingsWindow) Hide() {
	if w.win == nil || !w.visible {
		return
	}
	w.win.Canvas().Unfocus()
	w.win.Hide()
	w.visible = false
}

// Visible reports whether the window is on screen.
func (w *SettingsWindow) Visible() bool { return w.visible }

// Window is the Fyne window, or nil before the first Show.
func (w *SettingsWindow) Window() fyne.Window { return w.win }

// Page is the page currently selected.
func (w *SettingsWindow) Page() Page {
	if w.tabs == nil {
		return PageGeneral
	}
	sel := w.tabs.Selected()
	for p, it := range w.items {
		if it == sel {
			return Page(p)
		}
	}
	return PageGeneral
}

// Pages are the pages in the sidebar, in order.
func (w *SettingsWindow) Pages() []Page {
	if w.tabs == nil {
		return nil
	}
	var out []Page
	for _, it := range w.tabs.Items {
		for p, pit := range w.items {
			if it == pit {
				out = append(out, Page(p))
			}
		}
	}
	return out
}

// SetSettings shows settings changed elsewhere, such as in the settings
// file or from the tray. Changes made in the window show at once without
// it. A different model clears the language detected for the last one.
func (w *SettingsWindow) SetSettings(s config.Settings) {
	if s.SelectedModel != w.cur.SelectedModel {
		w.SetDetected("")
	}
	w.show(s)
}

// Settings are the settings the window shows.
func (w *SettingsWindow) Settings() config.Settings { return w.cur.Clone() }

func (w *SettingsWindow) create() {
	for p, page := range [pageCount]struct {
		icon  fyne.Resource
		build func() fyne.CanvasObject
	}{
		PageGeneral:     {theme.SettingsIcon(), w.generalPage},
		PageModels:      {theme.DownloadIcon(), w.modelsPage},
		PageAdvanced:    {theme.ListIcon(), w.advancedPage},
		PagePermissions: {theme.VisibilityIcon(), w.permissionsPage},
		PageHistory:     {theme.HistoryIcon(), w.historyPage},
		PageDebug:       {theme.WarningIcon(), w.debugPage},
		PageAbout:       {theme.InfoIcon(), w.aboutPage},
	} {
		w.col = &labelColumn{} // each page lines up its own labels
		w.items[p] = container.NewTabItemWithIcon(Page(p).String(), page.icon, page.build())
	}
	w.col = nil
	w.tabs = container.NewAppTabs()
	w.tabs.SetTabLocation(container.TabLocationLeading)
	w.tabs.OnSelected = func(it *container.TabItem) {
		if it == w.items[PageDebug] {
			w.refreshLogs()
		}
	}
	w.created = true
	w.show(w.cur)

	w.win = w.app.NewWindow(SettingsTitle)
	w.win.SetIcon(AppIcon())
	w.layout()
	w.win.Resize(fyne.NewSize(760, 560))
	w.win.CenterOnScreen()
	w.win.SetCloseIntercept(w.closeRequested)
	w.win.SetOnDropped(w.dropped)
	w.win.Canvas().AddShortcut(DebugShortcut, func(fyne.Shortcut) { w.ToggleDebugMode() })
}

// DebugShortcut toggles debug mode while the settings window has the
// keyboard: Cmd+Shift+D on macOS, Ctrl+Shift+D elsewhere.
var DebugShortcut = &desktop.CustomShortcut{
	KeyName:  fyne.KeyD,
	Modifier: fyne.KeyModifierShortcutDefault | fyne.KeyModifierShift,
}

// ToggleDebugMode turns the debug_mode setting on or off, which shows or
// hides the Debug page.
func (w *SettingsWindow) ToggleDebugMode() {
	on := !w.cur.DebugMode
	w.change(func(s *config.Settings) { s.DebugMode = on })
	if on {
		w.selectPage(PageDebug)
	}
}

// debugMode reports whether the Debug page belongs in the sidebar.
func (w *SettingsWindow) debugMode() bool { return w.opts.ForceDebug || w.cur.DebugMode }

// syncDebugPage puts the Debug page in the sidebar in debug mode and takes
// it out otherwise, keeping the page selected unless it was Debug.
func (w *SettingsWindow) syncDebugPage() {
	if w.tabs == nil {
		return
	}
	want := w.debugMode()
	shown := false
	for _, it := range w.tabs.Items {
		shown = shown || it == w.items[PageDebug]
	}
	if want == shown && len(w.tabs.Items) > 0 {
		return
	}
	current := w.Page()
	var items []*container.TabItem
	for p, it := range w.items {
		if Page(p) != PageDebug || want {
			items = append(items, it)
		}
	}
	w.tabs.SetItems(items)
	if current == PageDebug && !want {
		current = PageGeneral
	}
	w.tabs.Select(w.items[current])
}

func (w *SettingsWindow) selectPage(p Page) {
	if p == PageDebug && !w.debugMode() || p < 0 || p >= pageCount {
		p = PageGeneral
	}
	w.tabs.Select(w.items[p])
}

// Banner is a problem shown across the top of the settings window, with a
// button that fixes it and one that dismisses it.
type Banner struct {
	Error       bool // an error (red) rather than a warning (amber)
	Title       string
	Message     string
	ActionLabel string // no action button when empty
	OnAction    func()
	OnDismiss   func()
}

// Banner is the banner shown, or nil.
func (w *SettingsWindow) Banner() *Banner { return w.banner }

// SetBanner shows b across the top of the window, or removes the banner
// with nil.
func (w *SettingsWindow) SetBanner(b *Banner) {
	w.banner = b
	if w.win != nil {
		w.layout()
	}
}

// layout puts the banner, if any, above the pages, or above the step of
// onboarding on screen.
func (w *SettingsWindow) layout() {
	var content fyne.CanvasObject = w.tabs
	title := SettingsTitle
	if w.onboarding.active {
		content, title = w.onboarding.content, OnboardingTitle
	}
	w.win.SetTitle(title)
	if w.banner == nil {
		w.win.SetContent(content)
		return
	}
	w.win.SetContent(container.NewBorder(bannerView(w.banner), nil, nil, nil, content))
}

func bannerView(b *Banner) fyne.CanvasObject {
	title := widget.NewLabelWithStyle(b.Title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	message := paragraph(b.Message)
	buttons := container.NewHBox()
	if b.ActionLabel != "" {
		action := widget.NewButton(b.ActionLabel, b.OnAction)
		action.Importance = widget.HighImportance
		buttons.Add(action)
	}
	buttons.Add(widget.NewButton("Dismiss", b.OnDismiss))

	color := theme.Color(theme.ColorNameWarning)
	if b.Error {
		color = theme.Color(theme.ColorNameError)
	}
	stripe := canvas.NewRectangle(color)
	stripe.SetMinSize(fyne.NewSize(4, 0))
	body := container.NewVBox(title, message, container.NewHBox(layout.NewSpacer(), buttons))
	return container.NewPadded(container.NewBorder(nil, nil, stripe, nil, body))
}

// closeRequested runs when the user closes the window. A shortcut field
// still listening stops, so the global shortcuts come back.
func (w *SettingsWindow) closeRequested() {
	w.win.Canvas().Unfocus()
	w.win.Hide()
	w.visible = false
	if w.opts.OnClose != nil {
		w.opts.OnClose()
	}
}
