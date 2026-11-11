package ui

import (
	"bytes"
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
		StartDictation: count("start"),
		StopDictation:  count("stop"),
		Cancel:         count("cancel"),
		OpenModels:     count("models"),
		OpenSettings:   count("settings"),
		Quit:           count("quit"),
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
	want := []string{LabelStartDictation, LabelCancel, "---", LabelNoModel, "---", LabelSettings, "---", LabelQuit}
	if got := labels(host.menu); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("menu = %v, want %v", got, want)
	}
	if !findItem(t, host.menu, LabelStartDictation).Disabled || !findItem(t, host.menu, LabelCancel).Disabled {
		t.Error("dictation items must be disabled without a model")
	}
	click(t, findItem(t, host.menu, LabelNoModel))
	click(t, findItem(t, host.menu, LabelSettings))
	if c["models"] != 1 || c["settings"] != 1 {
		t.Errorf("calls = %v", c)
	}
}

func TestTrayWithModel(t *testing.T) {
	host, c := &fakeHost{}, calls{}
	NewTray(host, TrayOptions{Actions: c.actions(), Model: "Parakeet TDT 0.6B v2"})

	model := findItem(t, host.menu, LabelModelPrefix)
	if model.Label != "Model: Parakeet TDT 0.6B v2" {
		t.Errorf("model item = %q", model.Label)
	}
	if hasItem(host.menu, LabelNoModel) {
		t.Error("the download item should be gone once a model is ready")
	}
	click(t, model)
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
	tray := NewTray(host, TrayOptions{Actions: c.actions(), Model: "m"})

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
		if !dictate.Disabled || !findItem(t, host.menu, LabelCancel).Disabled {
			t.Errorf("%v: dictation items enabled without a model", s)
		}
	}
}

func TestTrayModelCanComeAndGo(t *testing.T) {
	host := &fakeHost{}
	tray := NewTray(host, TrayOptions{Actions: calls{}.actions()})
	tray.SetModel("Nemotron")
	if findItem(t, host.menu, LabelStartDictation).Disabled {
		t.Error("Start should be enabled once a model is ready")
	}
	tray.SetModel("")
	if !hasItem(host.menu, LabelNoModel) || !findItem(t, host.menu, LabelStartDictation).Disabled {
		t.Error("removing the model should bring back the download item")
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
	tray := NewTray(host, TrayOptions{Actions: calls{}.actions(), Model: "m"})
	menus, icons := host.menus, host.icons
	tray.SetState(TrayIdle)
	tray.SetModel("m")
	tray.SetDebug(false)
	if host.menus != menus || host.icons != icons {
		t.Errorf("unchanged updates touched the tray: %d menus, %d icons", host.menus-menus, host.icons-icons)
	}
	tray.SetModel("other")
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
		if themed == (s == TrayRecording) {
			t.Errorf("%v: themed %v; only the red recording icon should be unthemed", s, themed)
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
