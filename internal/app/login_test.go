package app

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/platform"
	"github.com/opragmatico/goscriber/internal/ui"
)

func TestMain(m *testing.M) {
	// No test may touch the real login item: on Linux, refreshing it would
	// point it at the test binary. Tests that need one install their own.
	useLogin(&fakeLogin{unsupported: true})
	os.Exit(m.Run())
}

// fakeLogin stands in for the OS's login item.
type fakeLogin struct {
	mu          sync.Mutex
	unsupported bool
	on          bool
	setErr      error // what SetLoginItem returns, leaving the login item as it is
	sets        []bool
	atLogin     bool // macOS opened the app as a login item
}

func (f *fakeLogin) supported() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unsupported {
		return fmt.Errorf("%w: only Goscriber.app can open at login", platform.ErrLoginItemUnsupported)
	}
	return nil
}

func (f *fakeLogin) enabled() (bool, error) {
	if err := f.supported(); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.on, nil
}

func (f *fakeLogin) set(on bool) error {
	if err := f.supported(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, on)
	if f.setErr != nil {
		return f.setErr
	}
	f.on = on
	return nil
}

func (f *fakeLogin) state() (on bool, sets []bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.on, slices.Clone(f.sets)
}

func (f *fakeLogin) launchedAtLogin() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.atLogin
}

func useLogin(f *fakeLogin) {
	loginItemSupported, loginItemEnabled, setLoginItem, launchedAtLogin = f.supported, f.enabled, f.set, f.launchedAtLogin
}

// withLogin makes f the login item for the rest of the test.
func withLogin(t *testing.T, f *fakeLogin) *fakeLogin {
	t.Helper()
	saved := []any{loginItemSupported, loginItemEnabled, setLoginItem, launchedAtLogin}
	useLogin(f)
	t.Cleanup(func() {
		loginItemSupported = saved[0].(func() error)
		loginItemEnabled = saved[1].(func() (bool, error))
		setLoginItem = saved[2].(func(bool) error)
		launchedAtLogin = saved[3].(func() bool)
	})
	return f
}

func autostart(sh *shell) bool { return sh.rt.Settings.Get().Autostart }

// At launch the setting takes the login item's state, which the user may
// have changed outside Goscriber; a login item that is on is written again,
// so it starts this executable.
func TestAutostartFollowsTheLoginItemAtLaunch(t *testing.T) {
	isolate(t)
	login := withLogin(t, &fakeLogin{on: true})
	sh, _ := newTestShell(t, &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}, Options{})
	if !autostart(sh) {
		t.Error("the login item is on, but the setting stayed off")
	}
	if on, sets := login.state(); !on || !slices.Equal(sets, []bool{true}) {
		t.Errorf("login item on %v, set %v; want it written again once", on, sets)
	}
}

func TestAutostartFollowsARemovedLoginItemAtLaunch(t *testing.T) {
	home := isolate(t)
	writeSettings(t, home, `{"schema_version": 1, "autostart": true}`)
	login := withLogin(t, &fakeLogin{})
	sh, _ := newTestShell(t, &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}, Options{})
	if autostart(sh) {
		t.Error("the login item was removed outside Goscriber, but the setting stayed on")
	}
	if _, sets := login.state(); len(sets) != 0 {
		t.Errorf("the login item was set %v; want it left alone", sets)
	}
}

// Without a login item to have, the setting is left as it is and the
// settings window says why.
func TestAutostartWithoutALoginItem(t *testing.T) {
	home := isolate(t)
	writeSettings(t, home, `{"schema_version": 1, "autostart": true}`)
	login := withLogin(t, &fakeLogin{unsupported: true})
	sh, q := newTestShell(t, &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}, Options{})
	if !autostart(sh) {
		t.Error("the setting was turned off with no login item to follow")
	}
	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.Autostart = false }); err != nil {
		t.Fatal(err)
	}
	q.drain()
	if _, sets := login.state(); len(sets) != 0 {
		t.Errorf("the login item was set %v", sets)
	}
	if got := loginItemUnavailable(); got != "launching at login is not supported: only Goscriber.app can open at login" {
		t.Errorf("the settings window would say %q", got)
	}
}

// Turning the setting on and off adds and removes the login item.
func TestAutostartSettingAddsAndRemovesTheLoginItem(t *testing.T) {
	isolate(t)
	login := withLogin(t, &fakeLogin{})
	sh, q := newTestShell(t, &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}, Options{})
	if got := loginItemUnavailable(); got != "" {
		t.Errorf("the settings window would say %q", got)
	}

	for _, want := range []bool{true, false} {
		if err := sh.rt.Settings.Update(func(s *config.Settings) { s.Autostart = want }); err != nil {
			t.Fatal(err)
		}
		q.waitUI(t, fmt.Sprintf("the login item turned %v", onOff(want)), func() bool {
			on, _ := login.state()
			return on == want
		})
	}
	if _, sets := login.state(); !slices.Equal(sets, []bool{true, false}) {
		t.Errorf("the login item was set %v", sets)
	}
	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.HistoryLimit = 9 }); err != nil {
		t.Fatal(err)
	}
	q.drain()
	if _, sets := login.state(); len(sets) != 2 {
		t.Errorf("another setting's change set the login item: %v", sets)
	}
	if sh.notices.Count(notice.CodeLoginItemFailed) != 0 {
		t.Errorf("notices %+v", sh.notices.Active())
	}
}

// When the OS refuses, a notice says why and the setting goes back to what
// the OS has.
func TestAutostartFailureRaisesANoticeAndRevertsTheSetting(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		action notice.ActionID
	}{
		{"refused", errors.New("platform: write the login item: permission denied"), ""},
		{"needs approval", platform.ErrLoginItemNeedsApproval, notice.ActionOpenLoginItems},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isolate(t)
			a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
			login := withLogin(t, &fakeLogin{setErr: tt.err})
			sh, q := newTestShell(t, a, Options{})
			if err := sh.rt.Settings.Update(func(s *config.Settings) { s.Autostart = true }); err != nil {
				t.Fatal(err)
			}
			q.waitUI(t, "the notice", func() bool { return sh.notices.Count(notice.CodeLoginItemFailed) == 1 })
			q.waitUI(t, "the setting to go back", func() bool { return !autostart(sh) })
			n := sh.notices.Active()[0]
			if n.Level != notice.LevelWarning || n.Action.ID != tt.action {
				t.Errorf("notice %+v", n)
			}
			if _, sets := login.state(); !slices.Equal(sets, []bool{true}) {
				t.Errorf("the login item was set %v, want one try", sets)
			}
			if tt.action != "" {
				sh.noticeAction(tt.action)
				if !slices.Contains(a.opened, platform.LoginItemsSettingsURL) {
					t.Errorf("the notice's button opened %v", a.opened)
				}
			}
		})
	}
}

func TestPlanLaunchAtLogin(t *testing.T) {
	def := config.Defaults()
	for _, tt := range []struct {
		name       string
		opts       Options
		supported  bool
		onboarding bool
		want       launch
	}{
		{"at login", Options{AtLogin: true}, true, false, launch{Tray: true, Hidden: true}},
		{"at login, onboarding", Options{AtLogin: true}, true, true, launch{Tray: true, Hidden: true}},
		// Without a tray the window is the only way in.
		{"at login, no tray", Options{AtLogin: true, NoTray: true}, true, false, launch{Window: true, Hidden: true}},
	} {
		if got := planLaunch(def, tt.opts, tt.supported, tt.onboarding); got != tt.want {
			t.Errorf("%s: %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

// macOS tells a launch at login only once the app has started: onboarding
// then waits behind the tray, as when starting hidden, and the missing
// model becomes the launch's notice.
func TestLaunchAtLoginOnMacOSStartsHidden(t *testing.T) {
	isolate(t)
	withLogin(t, &fakeLogin{atLogin: true})
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newFirstLaunchShell(t, a, Options{})
	if !sh.plan.Onboarding {
		t.Fatal("onboarding should be planned before macOS tells the launch at login")
	}
	sh.trusted = func() bool { return true }
	sh.started()
	q.runNext(t)
	if sh.settings.Visible() || sh.plan.Onboarding || !sh.plan.Hidden {
		t.Errorf("window visible %v, plan %+v; want no window", sh.settings.Visible(), sh.plan)
	}
	if sh.notices.Count(notice.CodeNoModel) != 1 {
		t.Errorf("notices %+v, want the no-model notice", sh.notices.Active())
	}
	item(t, a.menu, ui.LabelNoModel).Action()
	if step, on := sh.settings.Onboarding(); !on || step != ui.StepModel {
		t.Errorf("the tray opened onboarding %v on %v", on, step)
	}
}
