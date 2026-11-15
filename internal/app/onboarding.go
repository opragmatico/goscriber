package app

import (
	"runtime"

	"fyne.io/fyne/v2"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/ui"
)

// App preferences that remember onboarding across launches: whether it is
// over (finished or skipped), and otherwise the step the user last saw, so
// a quit midway resumes there.
const (
	prefOnboardingDone = "onboarding.done"
	prefOnboardingStep = "onboarding.step"
)

// pendingOnboarding returns the step onboarding resumes at, and false when
// it is over. Someone who already selected a model without it, from the
// command line or before onboarding existed, has nothing to set up, so it is
// marked over for them.
func pendingOnboarding(prefs fyne.Preferences, s config.Settings) (ui.OnboardingStep, bool) {
	if prefs.Bool(prefOnboardingDone) {
		return 0, false
	}
	name := prefs.String(prefOnboardingStep)
	if name == "" && s.SelectedModel != "" {
		prefs.SetBool(prefOnboardingDone, true)
		return 0, false
	}
	step, ok := ui.ParseOnboardingStep(name)
	if !ok {
		step = ui.StepWelcome
	}
	return step, true
}

// onboardingStepShown remembers the step on screen, unless onboarding is
// over and the user is going through it again from the About page.
func (sh *shell) onboardingStepShown(step ui.OnboardingStep) {
	if prefs := sh.app.Preferences(); !prefs.Bool(prefOnboardingDone) {
		prefs.SetString(prefOnboardingStep, step.String())
	}
}

// onboardingDone runs when the user finishes or skips onboarding: it will
// not open at launch again, and what its permissions step may not have asked
// for is asked for now.
func (sh *shell) onboardingDone(skipped bool) {
	prefs := sh.app.Preferences()
	prefs.SetBool(prefOnboardingDone, true)
	prefs.RemoveValue(prefOnboardingStep)
	sh.rt.Log.Info("onboarding over", "skipped", skipped, "model", sh.rt.Settings.Get().SelectedModel)
	sh.askPermissionsOnce()
}

// askPermissionsOnce asks for the missing macOS permissions in the
// background, the first time it is called in a launch.
func (sh *shell) askPermissionsOnce() {
	if runtime.GOOS != "darwin" {
		return
	}
	sh.askedPermissions.Do(func() { go sh.askPermissions() })
}

// openModels shows where to get a model: onboarding's model step while none
// is selected, the Models page otherwise. The tray's "No model: Download…"
// and the no_model notice's button lead here.
func (sh *shell) openModels() {
	if sh.rt.Settings.Get().SelectedModel == "" {
		sh.settings.ShowOnboarding(ui.StepModel)
		return
	}
	sh.settings.Show(ui.PageModels)
}
