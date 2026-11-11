package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/ui"
)

// plainApp is Fyne's headless test app, which has no system tray, with Quit
// counted and the icon kept.
type plainApp struct {
	fyne.App
	quits int
	icon  fyne.Resource
}

func (a *plainApp) Quit()                   { a.quits++ }
func (a *plainApp) SetIcon(r fyne.Resource) { a.icon = r }
func (a *plainApp) Icon() fyne.Resource     { return a.icon }

// trayApp adds a fake system tray, so it satisfies desktop.App.
type trayApp struct {
	plainApp
	menu     *fyne.Menu
	trayIcon fyne.Resource
}

func (a *trayApp) SetSystemTrayMenu(m *fyne.Menu)    { a.menu = m }
func (a *trayApp) SetSystemTrayIcon(r fyne.Resource) { a.trayIcon = r }
func (a *trayApp) SetSystemTrayWindow(fyne.Window)   {}

// uiQueue stands in for fyne.Do: the follower queues UI updates and the test
// runs them on its own goroutine, as the main thread would.
type uiQueue chan func()

func (q uiQueue) do(f func()) { q <- f }

func (q uiQueue) runNext(t *testing.T) {
	t.Helper()
	select {
	case f := <-q:
		f()
	case <-time.After(2 * time.Second):
		t.Fatal("no UI update after the settings change")
	}
}

func newTestShell(t *testing.T, a fyne.App, opts Options) (*shell, uiQueue) {
	t.Helper()
	rt := start(t, opts)
	sh := newShell(rt, a, opts)
	q := make(uiQueue, 8)
	sh.do = q.do
	sh.start()
	t.Cleanup(sh.stop)
	return sh, q
}

func item(t *testing.T, m *fyne.Menu, label string) *fyne.MenuItem {
	t.Helper()
	for _, it := range m.Items {
		if it.Label == label {
			return it
		}
	}
	t.Fatalf("no %q item in the tray menu", label)
	return nil
}

func TestPlanLaunch(t *testing.T) {
	def := config.Defaults()
	noTraySetting := def
	noTraySetting.ShowTrayIcon = false
	hiddenSetting := def
	hiddenSetting.StartHidden = true

	for _, tt := range []struct {
		name      string
		settings  config.Settings
		opts      Options
		supported bool
		want      launch
	}{
		{"defaults", def, Options{}, true, launch{Tray: true}},
		{"start hidden setting", hiddenSetting, Options{}, true, launch{Tray: true, Hidden: true}},
		{"--start-hidden", def, Options{StartHidden: true}, true, launch{Tray: true, Hidden: true}},
		{"--no-tray", def, Options{NoTray: true}, true, launch{Window: true}},
		{"show_tray_icon off", noTraySetting, Options{}, true, launch{Window: true}},
		{"no tray on this desktop", def, Options{}, false, launch{Window: true}},
		// Without a tray the window is the only way in, hidden or not.
		{"--no-tray --start-hidden", def, Options{NoTray: true, StartHidden: true}, true, launch{Window: true, Hidden: true}},
	} {
		if got := planLaunch(tt.settings, tt.opts, tt.supported); got != tt.want {
			t.Errorf("%s: %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestShellInstallsTrayAndOpensNoWindow(t *testing.T) {
	isolate(t)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	windows := len(a.Driver().AllWindows())
	sh, _ := newTestShell(t, a, Options{Version: "v0.1.0"})

	if a.menu == nil || a.trayIcon != ui.TrayIcon(ui.TrayIdle) {
		t.Fatalf("tray not installed: menu %v, icon %v", a.menu, a.trayIcon)
	}
	if a.Icon() != ui.AppIcon() {
		t.Error("the app icon should be set")
	}
	sh.started()
	if sh.settings.Window() != nil || len(a.Driver().AllWindows()) != windows {
		t.Error("no window should open at launch while the tray is shown")
	}
	if !item(t, a.menu, ui.LabelStartDictation).Disabled {
		t.Error("with no model, dictation must be disabled")
	}
}

func TestTrayOpensSettingsPages(t *testing.T) {
	isolate(t)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, _ := newTestShell(t, a, Options{})

	item(t, a.menu, ui.LabelNoModel).Action()
	if sh.settings.Window() == nil || sh.settings.Page() != ui.PageModels {
		t.Fatalf("No model: Download… should open the Models page (page %v)", sh.settings.Page())
	}
	item(t, a.menu, ui.LabelSettings).Action()
	if sh.settings.Page() != ui.PageGeneral {
		t.Errorf("Settings… opened page %v", sh.settings.Page())
	}

	sh.settingsClosed()
	if a.quits != 0 {
		t.Error("closing the window must not quit while the tray is shown")
	}
	item(t, a.menu, ui.LabelQuit).Action()
	if a.quits != 1 {
		t.Errorf("Quit called app.Quit %d times", a.quits)
	}
}

func TestTrayFollowsSettings(t *testing.T) {
	isolate(t)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})

	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.SelectedModel = "parakeet-tdt-0.6b-v2" }); err != nil {
		t.Fatal(err)
	}
	q.runNext(t)
	item(t, a.menu, ui.LabelModelPrefix+"parakeet-tdt-0.6b-v2")
	if item(t, a.menu, ui.LabelStartDictation).Disabled {
		t.Error("Start should be enabled once a model is selected")
	}

	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.DebugMode = true }); err != nil {
		t.Fatal(err)
	}
	q.runNext(t)
	debug := item(t, a.menu, ui.LabelDebug)
	item(t, debug.ChildMenu, ui.LabelTrayIconPrefix+"Recording").Action()
	if a.trayIcon != ui.TrayIcon(ui.TrayRecording) {
		t.Error("the debug menu should drive the tray icon")
	}
}

func TestDebugFlagShowsDebugMenu(t *testing.T) {
	isolate(t)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	newTestShell(t, a, Options{Debug: true})
	item(t, a.menu, ui.LabelDebug)
}

func TestDictationItemsAreLoggedUntilTheCoordinatorLands(t *testing.T) {
	home := isolate(t)
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m"}`)
	var stderr bytes.Buffer
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	newTestShell(t, a, Options{Stderr: &stderr})

	item(t, a.menu, ui.LabelStartDictation).Action()
	if !strings.Contains(stderr.String(), "dictation is not implemented yet") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestShellWithoutTrayOpensWindowAndQuitsOnClose(t *testing.T) {
	for _, tt := range []struct {
		name string
		app  fyne.App
		opts Options
	}{
		{"--no-tray", &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}, Options{NoTray: true, StartHidden: true}},
		{"no tray on this desktop", &plainApp{App: test.NewTempApp(t)}, Options{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isolate(t)
			sh, _ := newTestShell(t, tt.app, tt.opts)
			if ta, ok := tt.app.(*trayApp); ok && ta.menu != nil {
				t.Error("no tray menu should be installed")
			}
			sh.started()
			if sh.settings.Window() == nil || sh.settings.Page() != ui.PageGeneral {
				t.Fatal("the settings window should open at launch without a tray")
			}
			sh.settingsClosed()
			quits := 0
			switch a := tt.app.(type) {
			case *trayApp:
				quits = a.quits
			case *plainApp:
				quits = a.quits
			}
			if quits != 1 {
				t.Errorf("closing the only window called Quit %d times", quits)
			}
		})
	}
}

func TestRunReturnsWhenTheAppQuits(t *testing.T) {
	isolate(t)
	var stderr bytes.Buffer
	// The test app's Run returns at once, as if the user chose Quit.
	err := run(Options{Stderr: &stderr}, func() fyne.App {
		return &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"goscriber starting", "app shell ready", "goscriber stopped"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("%q missing from the log: %q", want, stderr.String())
		}
	}
}

func TestRunFailsWithoutSettingsDir(t *testing.T) {
	home := isolate(t)
	writeSettings(t, home, "")
	t.Setenv(config.HomeEnv, home+"/settings.json/nested") // a file where a directory should be
	err := run(Options{Stderr: &bytes.Buffer{}}, func() fyne.App {
		t.Fatal("the app should not be created when the runtime fails")
		return nil
	})
	if err == nil {
		t.Fatal("want an error")
	}
}

func writeSettings(t *testing.T, home, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
