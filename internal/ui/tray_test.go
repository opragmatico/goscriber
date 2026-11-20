package ui

import (
	"bytes"
	"runtime"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

type fakeHost struct {
	menu         *fyne.Menu
	icon         fyne.Resource
	menus, icons int
}

func (h *fakeHost) SetSystemTrayMenu(m *fyne.Menu)    { h.menu = m; h.menus++ }
func (h *fakeHost) SetSystemTrayIcon(r fyne.Resource) { h.icon = r; h.icons++ }

// calls counts how often each action ran.
type calls map[string]int

func (c calls) actions() TrayActions {
	count := func(name string) func() { return func() { c[name]++ } }
	return TrayActions{
		StartDictation:     count("start"),
		StopDictation:      count("stop"),
		Cancel:             count("cancel"),
		CopyLastTranscript: count("copy"),
		UseModel:           func(m TrayModel) { c["use "+m.ID+" "+m.Quant]++ },
		OpenModels:         count("models"),
		UnloadModel:        count("unload"),
		OpenSettings:       count("settings"),
		CheckForUpdates:    count("updates"),
		Quit:               count("quit"),
	}
}

var (
	trayParakeet = TrayModel{ID: "parakeet-tdt-0.6b-v2", Name: "Parakeet TDT 0.6B v2", Quant: "Q8_0"}
	trayNemotron = TrayModel{ID: "nemotron-3.5-asr-streaming-0.6b", Name: "Nemotron Streaming 3.5", Quant: "Q8_0"}
)

func sameLabels(t *testing.T, m *fyne.Menu, want ...string) {
	t.Helper()
	if got := labels(m); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("menu = %v, want %v", got, want)
	}
}

// findItem returns the item whose label is label, or starts with it when it
// ends in a space.
func findItem(t *testing.T, m *fyne.Menu, label string) *fyne.MenuItem {
	t.Helper()
	for _, it := range m.Items {
		if it.Label == label || (strings.HasSuffix(label, " ") && strings.HasPrefix(it.Label, label)) {
			return it
		}
	}
	t.Fatalf("no %q item in the menu: %v", label, labels(m))
	return nil
}

func hasItem(m *fyne.Menu, label string) bool {
	for _, it := range m.Items {
		if it.Label == label {
			return true
		}
	}
	return false
}

func labels(m *fyne.Menu) []string {
	var out []string
	for _, it := range m.Items {
		if it.IsSeparator {
			out = append(out, "---")
			continue
		}
		out = append(out, it.Label)
	}
	return out
}

func click(t *testing.T, it *fyne.MenuItem) {
	t.Helper()
	if it.Disabled {
		t.Fatalf("%q is disabled", it.Label)
	}
	if it.Action == nil {
		t.Fatalf("%q has no action", it.Label)
	}
	it.Action()
}

func TestTrayWithoutModelOffersDownload(t *testing.T) {
	host, c := &fakeHost{}, calls{}
	tray := NewTray(host, TrayOptions{Actions: c.actions()})

	if host.menu == nil || host.menu != tray.Menu() {
		t.Fatal("the menu was not installed")
	}
	if host.icon != TrayIcon(TrayIdle) {
		t.Error("the tray should start with the idle icon")
	}
	sameLabels(t, host.menu, LabelStartDictation, LabelCancel, LabelCopyLastTranscript, "---",
		LabelNoModel, LabelUnloadModel, "---", LabelSettings, LabelCheckForUpdates, "---", LabelQuit)
	if !findItem(t, host.menu, LabelStartDictation).Disabled || !findItem(t, host.menu, LabelCancel).Disabled {
		t.Error("dictation items must be disabled without a model")
	}
	if !findItem(t, host.menu, LabelUnloadModel).Disabled {
		t.Error("nothing to unload without a model")
	}
	click(t, findItem(t, host.menu, LabelNoModel))
	click(t, findItem(t, host.menu, LabelSettings))
	if c["models"] != 1 || c["settings"] != 1 {
		t.Errorf("calls = %v", c)
	}
}

func TestTrayWithModel(t *testing.T) {
	host, c := &fakeHost{}, calls{}
	NewTray(host, TrayOptions{Actions: c.actions(), Model: trayParakeet})

	model := findItem(t, host.menu, trayParakeet.Name)
	if model.ChildMenu == nil {
		t.Fatal("the model item should open a submenu")
	}
	if hasItem(host.menu, LabelNoModel) {
		t.Error("the download item should be gone once a model is ready")
	}
	// Before the downloaded models are listed, the submenu still leads to
	// the Models page.
	sameLabels(t, model.ChildMenu, LabelManageModels)
	click(t, findItem(t, model.ChildMenu, LabelManageModels))
	click(t, findItem(t, host.menu, LabelStartDictation))
	if !findItem(t, host.menu, LabelCancel).Disabled {
		t.Error("Cancel should be disabled while idle")
	}
	if c["models"] != 1 || c["start"] != 1 {
		t.Errorf("calls = %v", c)
	}
}

func TestTrayStatesChangeIconAndItems(t *testing.T) {
	host, c := &fakeHost{}, calls{}
	tray := NewTray(host, TrayOptions{Actions: c.actions(), Model: trayParakeet})

	tray.SetState(TrayRecording)
	if host.icon != TrayIcon(TrayRecording) || tray.State() != TrayRecording {
		t.Error("recording icon not shown")
	}
	click(t, findItem(t, host.menu, LabelStopDictation))
	click(t, findItem(t, host.menu, LabelCancel))

	tray.SetState(TrayTranscribing)
	if host.icon != TrayIcon(TrayTranscribing) {
		t.Error("transcribing icon not shown")
	}
	if !findItem(t, host.menu, LabelTranscribing).Disabled {
		t.Error("nothing to start or stop while transcribing")
	}
	click(t, findItem(t, host.menu, LabelCancel))

	tray.SetState(TrayIdle)
	if host.icon != TrayIcon(TrayIdle) || !hasItem(host.menu, LabelStartDictation) {
		t.Error("back to idle should restore the idle icon and Start item")
	}
	if c["stop"] != 1 || c["cancel"] != 2 {
		t.Errorf("calls = %v", c)
	}
}

func TestTrayWithoutModelKeepsDictationDisabledInEveryState(t *testing.T) {
	host := &fakeHost{}
	tray := NewTray(host, TrayOptions{Actions: calls{}.actions()})
	for _, s := range TrayStates {
		tray.SetState(s)
		dictate := host.menu.Items[0]
		if dictate.Label == LabelCopyLastTranscript {
			t.Fatalf("%v: no dictation item first", s)
		}
		if !dictate.Disabled || !findItem(t, host.menu, LabelCancel).Disabled {
			t.Errorf("%v: dictation items enabled without a model", s)
		}
	}
}

func TestTrayModelCanComeAndGo(t *testing.T) {
	host := &fakeHost{}
	tray := NewTray(host, TrayOptions{Actions: calls{}.actions()})
	tray.SetModel(trayNemotron)
	if findItem(t, host.menu, LabelStartDictation).Disabled {
		t.Error("Start should be enabled once a model is ready")
	}
	tray.SetModel(TrayModel{})
	if !hasItem(host.menu, LabelNoModel) || !findItem(t, host.menu, LabelStartDictation).Disabled {
		t.Error("removing the model should bring back the download item")
	}
}

// While a model loads, a disabled item under the model says so, and
// dictation stays available.
func TestTrayShowsTheModelLoading(t *testing.T) {
	host, c := &fakeHost{}, calls{}
	tray := NewTray(host, TrayOptions{Actions: c.actions(), Model: trayParakeet})

	tray.SetLoading("Parakeet TDT 0.6B v2")
	sameLabels(t, host.menu, LabelStartDictation, LabelCancel, LabelCopyLastTranscript, "---",
		trayParakeet.Name, "Loading Parakeet TDT 0.6B v2…", LabelUnloadModel, "---",
		LabelSettings, LabelCheckForUpdates, "---", LabelQuit)
	if !findItem(t, host.menu, LabelLoadingPrefix).Disabled {
		t.Error("the loading item is only a status")
	}
	if tray.Loading() != "Parakeet TDT 0.6B v2" {
		t.Errorf("Loading() = %q", tray.Loading())
	}
	click(t, findItem(t, host.menu, LabelStartDictation))
	if c["start"] != 1 {
		t.Errorf("calls = %v; dictation should stay available while the model loads", c)
	}

	tray.SetState(TrayRecording)
	if !hasItem(host.menu, "Loading Parakeet TDT 0.6B v2…") {
		t.Error("the loading item should stay while recording")
	}

	menu := host.menu
	tray.SetLoading("Parakeet TDT 0.6B v2")
	if host.menu != menu {
		t.Error("an unchanged loading model should not rebuild the menu")
	}
	tray.SetLoading("")
	if hasItem(host.menu, "Loading Parakeet TDT 0.6B v2…") {
		t.Error("the loading item should go once the load ends")
	}
}

func TestTrayQuit(t *testing.T) {
	host, c := &fakeHost{}, calls{}
	NewTray(host, TrayOptions{Actions: c.actions()})
	last := host.menu.Items[len(host.menu.Items)-1]
	if last.Label != LabelQuit || !last.IsQuit {
		t.Fatalf("last item %q (IsQuit %v), want the quit item", last.Label, last.IsQuit)
	}
	click(t, last)
	if c["quit"] != 1 {
		t.Errorf("calls = %v", c)
	}
}

func TestTrayDebugMenuDrivesTheIcon(t *testing.T) {
	host := &fakeHost{}
	tray := NewTray(host, TrayOptions{Actions: calls{}.actions()})
	if hasItem(host.menu, LabelDebug) {
		t.Fatal("no Debug menu outside debug mode")
	}

	tray.SetDebug(true)
	debug := findItem(t, host.menu, LabelDebug)
	if debug.ChildMenu == nil || len(debug.ChildMenu.Items) != len(TrayStates) {
		t.Fatalf("debug submenu = %+v", debug.ChildMenu)
	}
	if !findItem(t, debug.ChildMenu, LabelTrayIconPrefix+"Idle").Checked {
		t.Error("the current state should be checked")
	}

	click(t, findItem(t, debug.ChildMenu, LabelTrayIconPrefix+"Recording"))
	if tray.State() != TrayRecording || host.icon != TrayIcon(TrayRecording) {
		t.Errorf("state %v after choosing Recording", tray.State())
	}
	debug = findItem(t, host.menu, LabelDebug)
	if !findItem(t, debug.ChildMenu, LabelTrayIconPrefix+"Recording").Checked ||
		findItem(t, debug.ChildMenu, LabelTrayIconPrefix+"Idle").Checked {
		t.Error("the check mark should follow the state")
	}

	click(t, findItem(t, debug.ChildMenu, LabelTrayIconPrefix+"Transcribing"))
	if host.icon != TrayIcon(TrayTranscribing) {
		t.Error("transcribing icon not shown")
	}

	tray.SetDebug(false)
	if hasItem(host.menu, LabelDebug) {
		t.Error("the Debug menu should go when debug mode is off")
	}
}

func TestTraySkipsUnchangedUpdates(t *testing.T) {
	host := &fakeHost{}
	tray := NewTray(host, TrayOptions{Actions: calls{}.actions(), Model: trayParakeet})
	tray.SetModels([]TrayModel{trayParakeet, trayNemotron})
	menus, icons := host.menus, host.icons
	tray.SetState(TrayIdle)
	tray.SetModel(trayParakeet)
	tray.SetModels([]TrayModel{trayParakeet, trayNemotron})
	tray.SetResident(false)
	tray.SetDebug(false)
	if host.menus != menus || host.icons != icons {
		t.Errorf("unchanged updates touched the tray: %d menus, %d icons", host.menus-menus, host.icons-icons)
	}
	tray.SetModel(trayNemotron)
	if host.menus != menus+1 || host.icons != icons {
		t.Error("a model change should rebuild the menu only")
	}
}

func TestTrayIcons(t *testing.T) {
	seen := map[fyne.Resource]TrayState{}
	for _, s := range TrayStates {
		icon := TrayIcon(s)
		if prev, dup := seen[icon]; dup {
			t.Errorf("%v and %v share an icon", prev, s)
		}
		seen[icon] = s
		_, themed := icon.(*theme.ThemedResource)
		if runtime.GOOS != "linux" && themed == (s == TrayRecording) {
			t.Errorf("%v: themed %v; only the red recording icon should be unthemed", s, themed)
		}
	}
	// A Linux panel keeps its own colours, so no icon follows the app's
	// theme there: idle and transcribing are painted to show on any panel.
	for s, icon := range trayIconsFor("linux") {
		if _, themed := icon.(*theme.ThemedResource); themed {
			t.Errorf("Linux: %v is themed", s)
		}
		if s != TrayRecording && !strings.Contains(string(icon.Content()), `fill="#e8e8e8"`) {
			t.Errorf("Linux: %v is not painted for the panel", s)
		}
	}
	if TrayIcon(TrayState(99)) != TrayIcon(TrayIdle) {
		t.Error("unknown states should fall back to the idle icon")
	}
	for _, src := range [][]byte{appIconSVG, trayIdleSVG, trayRecordingSVG, trayTranscribingSVG} {
		if !bytes.HasPrefix(src, []byte("<svg ")) {
			t.Errorf("icon does not start with <svg: %.20q", src)
		}
	}
	if AppIcon().Name() != "goscriber.svg" {
		t.Errorf("app icon %q", AppIcon().Name())
	}
}

func TestTrayStateString(t *testing.T) {
	for s, want := range map[TrayState]string{
		TrayIdle: "idle", TrayRecording: "recording", TrayTranscribing: "transcribing", TrayState(7): "TrayState(7)",
	} {
		if s.String() != want {
			t.Errorf("%d.String() = %q, want %q", int(s), s.String(), want)
		}
	}
}

func TestTrayAlert(t *testing.T) {
	h := &fakeHost{}
	c := calls{}
	tr := NewTray(h, TrayOptions{Actions: c.actions()})
	opened := 0
	tr.SetAlert(&TrayAlert{Label: "⚠ No model downloaded", Action: func() { opened++ }})
	if h.icon != TrayAttentionIcon() {
		t.Error("an alert while idle should turn the icon amber")
	}
	if got := labels(h.menu); got[0] != "⚠ No model downloaded" || got[1] != "---" {
		t.Fatalf("menu %v, want the alert first", got)
	}
	click(t, h.menu.Items[0])
	if opened != 1 {
		t.Error("the alert item should run its action")
	}

	// Recording and transcribing keep their own icons; idle shows the alert
	// again.
	tr.SetState(TrayRecording)
	if h.icon != TrayIcon(TrayRecording) {
		t.Error("recording should show the recording icon even with an alert")
	}
	tr.SetState(TrayIdle)
	if h.icon != TrayAttentionIcon() {
		t.Error("back to idle, the alert icon should return")
	}

	menus := h.menus
	tr.SetAlert(nil)
	if h.icon != TrayIcon(TrayIdle) || hasItem(h.menu, "⚠ No model downloaded") || tr.Alert() != nil {
		t.Errorf("clearing the alert: icon %v, menu %v", h.icon.Name(), labels(h.menu))
	}
	tr.SetAlert(nil)
	if h.menus != menus+1 {
		t.Error("clearing an absent alert should not rebuild the menu")
	}
	if !bytes.HasPrefix(trayAttentionSVG, []byte("<svg ")) {
		t.Error("the attention icon is not an SVG")
	}
}

// The menu follows Handy's: the version, Copy Last Transcript, the model
// with its submenu and Unload Model, Settings… and Check for Updates…, Quit;
// with dictation's own items alongside.
func TestTrayMenuLayout(t *testing.T) {
	host, c := &fakeHost{}, calls{}
	tray := NewTray(host, TrayOptions{Actions: c.actions(), Version: "v1.2.3", Model: trayParakeet})
	tray.SetModels([]TrayModel{trayParakeet, trayNemotron})
	tray.SetResident(true)

	sameLabels(t, host.menu, "Goscriber v1.2.3", "---",
		LabelStartDictation, LabelCancel, LabelCopyLastTranscript, "---",
		trayParakeet.Name, LabelUnloadModel, "---",
		LabelSettings, LabelCheckForUpdates, "---",
		LabelQuit)
	version := findItem(t, host.menu, "Goscriber v1.2.3")
	if !version.Disabled {
		t.Error("the version is only a heading")
	}
	if s := findItem(t, host.menu, LabelSettings).Shortcut; s == nil || s.ShortcutName() == "" {
		t.Error("Settings… should keep its shortcut")
	}

	click(t, findItem(t, host.menu, LabelCopyLastTranscript))
	click(t, findItem(t, host.menu, LabelCheckForUpdates))
	click(t, findItem(t, host.menu, LabelUnloadModel))
	if c["copy"] != 1 || c["updates"] != 1 || c["unload"] != 1 {
		t.Errorf("calls = %v", c)
	}

	// An alert goes under the version.
	tray.SetAlert(&TrayAlert{Label: "⚠ Alert"})
	if got := labels(host.menu); got[0] != "Goscriber v1.2.3" || got[2] != "⚠ Alert" || got[3] != "---" {
		t.Errorf("menu %v, want the alert under the version", got)
	}
}

// The model submenu lists the downloaded models, checks the selected one,
// and switches to another with its quantisation.
func TestTrayModelSubmenuSwitchesModels(t *testing.T) {
	host, c := &fakeHost{}, calls{}
	tray := NewTray(host, TrayOptions{Actions: c.actions(), Model: TrayModel{ID: trayParakeet.ID, Name: trayParakeet.Name}})
	tray.SetModels([]TrayModel{trayParakeet, trayNemotron})

	sub := findItem(t, host.menu, trayParakeet.Name).ChildMenu
	sameLabels(t, sub, trayParakeet.Name, trayNemotron.Name, "---", LabelManageModels)
	if !findItem(t, sub, trayParakeet.Name).Checked || findItem(t, sub, trayNemotron.Name).Checked {
		t.Error("only the selected model should be checked")
	}

	click(t, findItem(t, sub, trayParakeet.Name))
	click(t, findItem(t, sub, trayNemotron.Name))
	if len(c) != 1 || c["use "+trayNemotron.ID+" Q8_0"] != 1 {
		t.Errorf("calls = %v; want only the switch to Nemotron", c)
	}

	tray.SetModel(TrayModel{ID: trayNemotron.ID, Name: trayNemotron.Name})
	sub = findItem(t, host.menu, trayNemotron.Name).ChildMenu
	if findItem(t, sub, trayParakeet.Name).Checked || !findItem(t, sub, trayNemotron.Name).Checked {
		t.Error("the check should follow the selected model")
	}
}

// Downloaded models with none selected: the submenu asks for a choice, and
// dictation waits for it.
func TestTrayAsksForAModelWhenNoneIsSelected(t *testing.T) {
	host, c := &fakeHost{}, calls{}
	tray := NewTray(host, TrayOptions{Actions: c.actions()})
	tray.SetModels([]TrayModel{trayNemotron})

	if hasItem(host.menu, LabelNoModel) {
		t.Error("with a model downloaded, the menu should not offer the download")
	}
	sub := findItem(t, host.menu, LabelChooseModel).ChildMenu
	if sub == nil {
		t.Fatal("no model submenu")
	}
	if findItem(t, sub, trayNemotron.Name).Checked {
		t.Error("nothing is selected")
	}
	if !findItem(t, host.menu, LabelStartDictation).Disabled {
		t.Error("dictation needs a selected model")
	}
	click(t, findItem(t, sub, trayNemotron.Name))
	if c["use "+trayNemotron.ID+" Q8_0"] != 1 {
		t.Errorf("calls = %v", c)
	}

	tray.SetModels(nil)
	if !hasItem(host.menu, LabelNoModel) {
		t.Error("with nothing downloaded, the download item should return")
	}
}

// Unload Model is there only for a model in memory that nothing is using.
func TestTrayUnloadModel(t *testing.T) {
	host, c := &fakeHost{}, calls{}
	tray := NewTray(host, TrayOptions{Actions: c.actions(), Model: trayParakeet})
	if !findItem(t, host.menu, LabelUnloadModel).Disabled {
		t.Error("nothing to unload before a model is in memory")
	}

	tray.SetResident(true)
	click(t, findItem(t, host.menu, LabelUnloadModel))
	if c["unload"] != 1 {
		t.Errorf("calls = %v", c)
	}

	for _, s := range []TrayState{TrayRecording, TrayTranscribing} {
		tray.SetState(s)
		if !findItem(t, host.menu, LabelUnloadModel).Disabled {
			t.Errorf("%v: the model is in use", s)
		}
	}
	tray.SetState(TrayIdle)
	tray.SetLoading(trayParakeet.Name)
	if !findItem(t, host.menu, LabelUnloadModel).Disabled {
		t.Error("a model loading cannot be unloaded")
	}
	tray.SetLoading("")
	tray.SetResident(false)
	if !findItem(t, host.menu, LabelUnloadModel).Disabled {
		t.Error("an unloaded model cannot be unloaded again")
	}
}
