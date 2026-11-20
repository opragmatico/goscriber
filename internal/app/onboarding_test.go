package app

import (
	"runtime"
	"slices"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/platform"
	"github.com/opragmatico/goscriber/internal/ui"
)

func TestPendingOnboarding(t *testing.T) {
	def := config.Defaults()
	withModel := def
	withModel.SelectedModel = "parakeet-tdt-0.6b-v2"

	for _, tt := range []struct {
		name     string
		done     bool
		step     string
		settings config.Settings
		want     ui.OnboardingStep
		pending  bool
		markDone bool
	}{
		{"first launch", false, "", def, ui.StepWelcome, true, false},
		{"quit midway", false, "model", def, ui.StepModel, true, false},
		{"quit midway with a model", false, "try", withModel, ui.StepTry, true, false},
		{"unknown step", false, "later", def, ui.StepWelcome, true, false},
		{"finished or skipped", true, "", def, 0, false, false},
		// Set up from the command line, or before onboarding existed.
		{"model already selected", false, "", withModel, 0, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			prefs := test.NewTempApp(t).Preferences()
			prefs.SetBool(prefOnboardingDone, tt.done)
			prefs.SetString(prefOnboardingStep, tt.step)
			step, pending := pendingOnboarding(prefs, tt.settings)
			if pending != tt.pending || pending && step != tt.want {
				t.Errorf("step %v, pending %v; want %v, %v", step, pending, tt.want, tt.pending)
			}
			if tt.markDone && !prefs.Bool(prefOnboardingDone) {
				t.Error("onboarding should be marked over")
			}
		})
	}
}

// At the first launch onboarding opens instead of the launch's notice and
// permission prompts. Each step is remembered, so a quit resumes there, and
// skipping it ends it for good, asks for the permissions and leaves the app
// with no model: the first shortcut press then raises the "no model
// downloaded" notice.
func TestFirstLaunchOnboarding(t *testing.T) {
	isolate(t)
	testShortcuts.reset()
	testPermissions.reset(perms{platform.PermMicrophone: platform.StatusNotDetermined}, perms{platform.PermMicrophone: platform.StatusGranted})
	t.Cleanup(func() { testPermissions.reset(nil, nil) })
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newFirstLaunchShell(t, a, Options{})
	sh.trusted = func() bool { return true } // whatever the machine running the test has granted
	sh.started()
	q.drain()

	if step, on := sh.settings.Onboarding(); !on || step != ui.StepWelcome || !sh.settings.Visible() {
		t.Fatalf("onboarding %v on %v, visible %v", on, step, sh.settings.Visible())
	}
	if len(a.notifications) != 0 || sh.notices.Count(notice.CodeNoModel) != 0 {
		t.Errorf("onboarding is about the missing model already: notifications %+v", a.notifications)
	}
	if asked := testPermissions.asked(); len(asked) != 0 {
		t.Errorf("asked for %v at launch; onboarding's permissions step asks", asked)
	}

	sh.settings.ShowOnboarding(ui.StepPermissions)
	q.drain()
	if got := a.Preferences().String(prefOnboardingStep); got != "permissions" {
		t.Errorf("remembered step %q", got)
	}
	if step, ok := pendingOnboarding(a.Preferences(), sh.rt.Settings.Get()); !ok || step != ui.StepPermissions {
		t.Errorf("the next launch would resume at %v (%v)", step, ok)
	}

	sh.settings.Show(ui.PageAbout) // the user wanders off...
	sh.settings.ShowOnboarding(ui.StepWelcome)
	skip(t, sh)
	if !a.Preferences().Bool(prefOnboardingDone) || a.Preferences().String(prefOnboardingStep) != "" {
		t.Error("skipping should end onboarding for good")
	}
	if _, ok := pendingOnboarding(a.Preferences(), sh.rt.Settings.Get()); ok {
		t.Error("onboarding would open again at the next launch")
	}
	if runtime.GOOS == "darwin" {
		q.waitUI(t, "the permissions to be asked for", func() bool {
			return slices.Contains(testPermissions.asked(), platform.PermMicrophone)
		})
	}

	testShortcuts.waitLog(t, "+"+defaultTalk(t), "+"+defaultPolish(t))
	testShortcuts.press(t, defaultTalk(t))
	q.waitUI(t, "the no-model notice", func() bool { return sh.notices.Count(notice.CodeNoModel) == 1 })
	q.drain()
	b := sh.settings.Banner()
	if b == nil || b.Title != "No model downloaded" {
		t.Fatalf("banner %+v", b)
	}
	b.OnAction()
	if step, on := sh.settings.Onboarding(); !on || step != ui.StepModel {
		t.Errorf("the notice's button should lead back to the model step (%v, %v)", step, on)
	}
}

// skip presses onboarding's Skip setup.
func skip(t *testing.T, sh *shell) {
	t.Helper()
	content := sh.settings.Window().Content()
	content.Resize(fyne.NewSize(760, 560))
	for _, o := range test.LaidOutObjects(content) {
		if b, ok := o.(*widget.Button); ok && b.Visible() && b.Text == ui.LabelSkipSetup {
			test.Tap(b)
			return
		}
	}
	t.Fatal("no Skip setup button")
}

// Starting hidden behind the tray leaves onboarding closed, with the launch
// notice as before; the tray's "No model: Download…" opens it.
func TestOnboardingWaitsWhenStartingHidden(t *testing.T) {
	isolate(t)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newFirstLaunchShell(t, a, Options{StartHidden: true})
	sh.trusted = func() bool { return true }
	sh.started()
	q.runNext(t)
	if sh.settings.Visible() {
		t.Error("no window should open when starting hidden")
	}
	if len(a.notifications) != 1 {
		t.Errorf("notifications %+v, want the launch's no-model notice", a.notifications)
	}
	item(t, a.menu, ui.LabelNoModel).Action()
	if step, on := sh.settings.Onboarding(); !on || step != ui.StepModel {
		t.Errorf("the tray opened onboarding %v on %v", on, step)
	}
}
