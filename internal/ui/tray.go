package ui

import (
	"fmt"
	"log/slog"
	"slices"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

// TrayState is the dictation state the tray icon shows.
type TrayState int

// Tray states. The coordinator's later steps (post-processing, output) show as
// TrayTranscribing.
const (
	TrayIdle TrayState = iota
	TrayRecording
	TrayTranscribing
)

// TrayStates lists every state, in the order the debug menu shows them.
var TrayStates = []TrayState{TrayIdle, TrayRecording, TrayTranscribing}

func (s TrayState) String() string {
	switch s {
	case TrayIdle:
		return "idle"
	case TrayRecording:
		return "recording"
	case TrayTranscribing:
		return "transcribing"
	default:
		return fmt.Sprintf("TrayState(%d)", int(s))
	}
}

func (s TrayState) title() string {
	switch s {
	case TrayRecording:
		return "Recording"
	case TrayTranscribing:
		return "Transcribing"
	default:
		return "Idle"
	}
}

// TrayHost is the part of desktop.App the tray needs, so tests can supply a
// fake.
type TrayHost interface {
	SetSystemTrayMenu(menu *fyne.Menu)
	SetSystemTrayIcon(icon fyne.Resource)
}

var _ TrayHost = desktop.App(nil)

// Menu labels, shared with tests.
const (
	LabelVersionPrefix      = "Goscriber " // then the version
	LabelStartDictation     = "Start Dictation"
	LabelStopDictation      = "Stop Dictation"
	LabelTranscribing       = "Transcribing…"
	LabelCancel             = "Cancel"
	LabelCopyLastTranscript = "Copy Last Transcript"
	LabelNoModel            = "No model: Download…"
	LabelChooseModel        = "Choose a Model"
	LabelManageModels       = "Manage Models…"
	LabelLoadingPrefix      = "Loading " // then the model's name and "…"
	LabelUnloadModel        = "Unload Model"
	LabelSettings           = "Settings…"
	LabelCheckForUpdates    = "Check for Updates…"
	LabelDebug              = "Debug"
	LabelTrayIconPrefix     = "Tray Icon: "
	LabelQuit               = "Quit Goscriber"
)

// TrayModel is a speech model the tray names: the one selected, or one of the
// downloaded ones its model submenu offers.
type TrayModel struct {
	ID   string // the catalog ID
	Name string // the display name
	// Quant is the quantisation to use when the model is chosen from the
	// submenu: a downloaded one.
	Quant string
}

// TrayActions run on the Fyne main thread when their menu item is chosen. A
// nil action makes its item do nothing, except Quit, which then falls back to
// Fyne's own quit.
type TrayActions struct {
	StartDictation     func()
	StopDictation      func()
	Cancel             func()
	CopyLastTranscript func()
	// UseModel selects a downloaded model from the model submenu.
	UseModel        func(m TrayModel)
	OpenModels      func() // "Manage Models…" and "No model: Download…"
	UnloadModel     func()
	OpenSettings    func()
	CheckForUpdates func()
	Quit            func()
}

// TrayOptions is how the tray starts.
type TrayOptions struct {
	Actions TrayActions
	// Version heads the menu, after the app's name; "" leaves the heading
	// out.
	Version string
	// Model is the model dictation will use; the zero TrayModel means none
	// is ready. Without one the dictation items are disabled and the menu
	// offers the download instead.
	Model TrayModel
	// Debug adds a Debug submenu that forces the icon state.
	Debug bool
}

// TrayAlert is a problem the tray shows until it is fixed: the idle icon
// turns amber and the menu starts with Label, under the version, which runs
// Action.
type TrayAlert struct {
	Label  string
	Action func()
}

// Tray owns the menu bar / system tray icon and its menu. Its methods must be
// called on the Fyne main thread; other goroutines go through fyne.Do.
type Tray struct {
	host     TrayHost
	actions  TrayActions
	version  string
	state    TrayState
	model    TrayModel   // the selected model; zero when none is ready
	models   []TrayModel // the downloaded models
	loading  string      // the model loading, or ""
	resident bool        // a model is in memory
	debug    bool
	alert    *TrayAlert
	menu     *fyne.Menu
}

// NewTray installs the menu and the idle icon on host.
func NewTray(host TrayHost, opts TrayOptions) *Tray {
	t := &Tray{host: host, actions: opts.Actions, version: opts.Version, model: opts.Model, debug: opts.Debug}
	// Fyne starts the tray with the first menu, so the menu goes first.
	t.refreshMenu()
	t.host.SetSystemTrayIcon(t.icon())
	return t
}

// icon is the icon for the current state: amber while idle with an alert.
func (t *Tray) icon() fyne.Resource {
	if t.state == TrayIdle && t.alert != nil {
		return TrayAttentionIcon()
	}
	return TrayIcon(t.state)
}

// Alert is the alert shown, or nil.
func (t *Tray) Alert() *TrayAlert { return t.alert }

// SetAlert shows a problem in the tray, or clears it with nil.
func (t *Tray) SetAlert(a *TrayAlert) {
	if a == nil && t.alert == nil {
		return
	}
	t.alert = a
	t.host.SetSystemTrayIcon(t.icon())
	t.refreshMenu()
}

// State is the state the icon shows.
func (t *Tray) State() TrayState { return t.state }

// Menu is the menu currently installed.
func (t *Tray) Menu() *fyne.Menu { return t.menu }

// SetState changes the icon and the dictation items.
func (t *Tray) SetState(s TrayState) {
	if s == t.state {
		return
	}
	t.state = s
	t.host.SetSystemTrayIcon(t.icon())
	t.refreshMenu()
}

// SetModel sets the model dictation will use; the zero TrayModel means none
// is ready.
func (t *Tray) SetModel(m TrayModel) {
	if m == t.model {
		return
	}
	t.model = m
	t.refreshMenu()
}

// SetModels sets the downloaded models the model submenu offers, in the
// order it lists them.
func (t *Tray) SetModels(ms []TrayModel) {
	if slices.Equal(ms, t.models) {
		return
	}
	t.models = slices.Clone(ms)
	t.refreshMenu()
}

// SetResident says whether a model is in memory, which Unload Model frees.
func (t *Tray) SetResident(on bool) {
	if on == t.resident {
		return
	}
	t.resident = on
	t.refreshMenu()
}

// SetLoading shows that model (a display name) is loading, or with "" that
// none is. Dictation stays available meanwhile: a recording started during
// the load is transcribed once it ends.
func (t *Tray) SetLoading(model string) {
	if model == t.loading {
		return
	}
	t.loading = model
	t.refreshMenu()
}

// Loading is the model the menu shows loading, or "".
func (t *Tray) Loading() string { return t.loading }

// SetDebug shows or hides the Debug submenu.
func (t *Tray) SetDebug(on bool) {
	if on == t.debug {
		return
	}
	t.debug = on
	t.refreshMenu()
}

// refreshMenu installs a freshly built menu. Fyne rebuilds the native menu on
// every call, so a new menu is as cheap as editing the old one in place.
func (t *Tray) refreshMenu() {
	t.menu = t.buildMenu()
	t.host.SetSystemTrayMenu(t.menu)
}

func (t *Tray) buildMenu() *fyne.Menu {
	a := t.actions
	ready := t.model.ID != ""

	var dictate *fyne.MenuItem
	switch t.state {
	case TrayRecording:
		dictate = fyne.NewMenuItem(LabelStopDictation, a.StopDictation)
	case TrayTranscribing:
		dictate = fyne.NewMenuItem(LabelTranscribing, nil)
		dictate.Disabled = true
	default:
		dictate = fyne.NewMenuItem(LabelStartDictation, a.StartDictation)
	}
	dictate.Disabled = dictate.Disabled || !ready

	cancel := fyne.NewMenuItem(LabelCancel, a.Cancel)
	cancel.Disabled = !ready || t.state == TrayIdle

	// A model in use, being loaded or transcribing stays: the engine
	// would refuse.
	unload := fyne.NewMenuItem(LabelUnloadModel, a.UnloadModel)
	unload.Disabled = !t.resident || t.loading != "" || t.state != TrayIdle

	settings := fyne.NewMenuItem(LabelSettings, a.OpenSettings)
	settings.Shortcut = &desktop.CustomShortcut{KeyName: fyne.KeyComma, Modifier: fyne.KeyModifierShortcutDefault}

	quit := fyne.NewMenuItem(LabelQuit, a.Quit)
	quit.IsQuit = true
	quit.Shortcut = &desktop.CustomShortcut{KeyName: fyne.KeyQ, Modifier: fyne.KeyModifierShortcutDefault}

	var items []*fyne.MenuItem
	if t.version != "" {
		version := fyne.NewMenuItem(LabelVersionPrefix+t.version, nil)
		version.Disabled = true
		items = append(items, version, fyne.NewMenuItemSeparator())
	}
	if t.alert != nil {
		items = append(items, fyne.NewMenuItem(t.alert.Label, t.alert.Action), fyne.NewMenuItemSeparator())
	}
	items = append(items,
		dictate,
		cancel,
		fyne.NewMenuItem(LabelCopyLastTranscript, a.CopyLastTranscript),
		fyne.NewMenuItemSeparator(),
		t.modelItem(),
	)
	if t.loading != "" {
		loading := fyne.NewMenuItem(LabelLoadingPrefix+t.loading+"…", nil)
		loading.Disabled = true
		items = append(items, loading)
	}
	items = append(items,
		unload,
		fyne.NewMenuItemSeparator(),
		settings,
		fyne.NewMenuItem(LabelCheckForUpdates, a.CheckForUpdates),
	)
	if t.debug {
		items = append(items, t.debugMenu())
	}
	items = append(items, fyne.NewMenuItemSeparator(), quit)
	return fyne.NewMenu("Goscriber", items...)
}

// modelItem names the selected model, with a submenu that switches to
// another downloaded one or opens the Models page. With no model selected or
// downloaded it offers the download instead.
func (t *Tray) modelItem() *fyne.MenuItem {
	if t.model.ID == "" && len(t.models) == 0 {
		return fyne.NewMenuItem(LabelNoModel, t.actions.OpenModels)
	}
	children := make([]*fyne.MenuItem, 0, len(t.models)+2)
	for _, m := range t.models {
		item := fyne.NewMenuItem(m.Name, func() {
			if m.ID != t.model.ID && t.actions.UseModel != nil {
				t.actions.UseModel(m)
			}
		})
		item.Checked = m.ID == t.model.ID
		children = append(children, item)
	}
	if len(children) > 0 {
		children = append(children, fyne.NewMenuItemSeparator())
	}
	children = append(children, fyne.NewMenuItem(LabelManageModels, t.actions.OpenModels))

	label := t.model.Name
	if t.model.ID == "" {
		label = LabelChooseModel
	}
	item := fyne.NewMenuItem(label, nil)
	item.ChildMenu = fyne.NewMenu(label, children...)
	return item
}

// debugMenu forces the icon state, so every icon can be checked before the
// dictation coordinator drives them.
func (t *Tray) debugMenu() *fyne.MenuItem {
	items := make([]*fyne.MenuItem, 0, len(TrayStates))
	for _, s := range TrayStates {
		item := fyne.NewMenuItem(LabelTrayIconPrefix+s.title(), func() {
			slog.Debug("tray state set from the debug menu", "state", s.String())
			t.SetState(s)
		})
		item.Checked = s == t.state
		items = append(items, item)
	}
	debug := fyne.NewMenuItem(LabelDebug, nil)
	debug.ChildMenu = fyne.NewMenu(LabelDebug, items...)
	return debug
}
