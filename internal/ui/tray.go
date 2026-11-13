package ui

import (
	"fmt"
	"log/slog"

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
	LabelStartDictation = "Start Dictation"
	LabelStopDictation  = "Stop Dictation"
	LabelTranscribing   = "Transcribing…"
	LabelCancel         = "Cancel"
	LabelNoModel        = "No model: Download…"
	LabelModelPrefix    = "Model: "
	LabelSettings       = "Settings…"
	LabelDebug          = "Debug"
	LabelTrayIconPrefix = "Tray Icon: "
	LabelQuit           = "Quit Goscriber"
)

// TrayActions run on the Fyne main thread when their menu item is chosen. A
// nil action makes its item do nothing, except Quit, which then falls back to
// Fyne's own quit.
type TrayActions struct {
	StartDictation func()
	StopDictation  func()
	Cancel         func()
	OpenModels     func() // "Model: …" and "No model: Download…"
	OpenSettings   func()
	Quit           func()
}

// TrayOptions is how the tray starts.
type TrayOptions struct {
	Actions TrayActions
	// Model is the display name of the model dictation will use, or "" when no
	// model is ready. Without one the dictation items are disabled and the menu
	// offers the download instead.
	Model string
	// Debug adds a Debug submenu that forces the icon state.
	Debug bool
}

// TrayAlert is a problem the tray shows until it is fixed: the idle icon
// turns amber and the menu starts with Label, which runs Action.
type TrayAlert struct {
	Label  string
	Action func()
}

// Tray owns the menu bar / system tray icon and its menu. Its methods must be
// called on the Fyne main thread; other goroutines go through fyne.Do.
type Tray struct {
	host    TrayHost
	actions TrayActions
	state   TrayState
	model   string
	debug   bool
	alert   *TrayAlert
	menu    *fyne.Menu
}

// NewTray installs the menu and the idle icon on host.
func NewTray(host TrayHost, opts TrayOptions) *Tray {
	t := &Tray{host: host, actions: opts.Actions, model: opts.Model, debug: opts.Debug}
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

// SetModel sets the display name of the ready model; "" means none is ready.
func (t *Tray) SetModel(name string) {
	if name == t.model {
		return
	}
	t.model = name
	t.refreshMenu()
}

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
	ready := t.model != ""

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

	model := fyne.NewMenuItem(LabelNoModel, a.OpenModels)
	if ready {
		model.Label = LabelModelPrefix + t.model
	}

	settings := fyne.NewMenuItem(LabelSettings, a.OpenSettings)
	settings.Shortcut = &desktop.CustomShortcut{KeyName: fyne.KeyComma, Modifier: fyne.KeyModifierShortcutDefault}

	quit := fyne.NewMenuItem(LabelQuit, a.Quit)
	quit.IsQuit = true
	quit.Shortcut = &desktop.CustomShortcut{KeyName: fyne.KeyQ, Modifier: fyne.KeyModifierShortcutDefault}

	var items []*fyne.MenuItem
	if t.alert != nil {
		items = append(items, fyne.NewMenuItem(t.alert.Label, t.alert.Action), fyne.NewMenuItemSeparator())
	}
	items = append(items,
		dictate,
		cancel,
		fyne.NewMenuItemSeparator(),
		model,
		fyne.NewMenuItemSeparator(),
		settings,
	)
	if t.debug {
		items = append(items, t.debugMenu())
	}
	items = append(items, fyne.NewMenuItemSeparator(), quit)
	return fyne.NewMenu("Goscriber", items...)
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
