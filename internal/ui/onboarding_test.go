package ui

import (
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/config"
)

// onboardingWindow is a test window that records what it tells the app
// about onboarding.
type onboardingWindow struct {
	*testWindow
	steps []OnboardingStep
	done  []bool // the skipped argument of each OnOnboardingDone
}

func newOnboardingWindow(t *testing.T) *onboardingWindow {
	t.Helper()
	ow := &onboardingWindow{}
	ow.testWindow = newTestWindow(t, func(o *SettingsOptions) {
		o.OnOnboardingStep = func(s OnboardingStep) { ow.steps = append(ow.steps, s) }
		o.OnOnboardingDone = func(skipped bool) { ow.done = append(ow.done, skipped) }
	})
	return ow
}

// showOnboarding opens onboarding on step and runs the background reads it
// starts.
func (tw *testWindow) showOnboarding(step OnboardingStep) {
	tw.ShowOnboarding(step)
	tw.q.drain()
}

// stepObjects are the objects drawn on the step on screen.
func (tw *testWindow) stepObjects() []fyne.CanvasObject {
	content := tw.onboarding.content
	content.Resize(fyne.NewSize(700, 2000))
	return test.LaidOutObjects(content)
}

// stepText joins the text of every visible label and button on the step.
func (tw *testWindow) stepText() string {
	var out []string
	for _, o := range tw.stepObjects() {
		switch o := o.(type) {
		case *widget.Label:
			if o.Visible() {
				out = append(out, o.Text)
			}
		case *widget.Button:
			if o.Visible() {
				out = append(out, "["+o.Text+"]")
			}
		case *widget.Card:
			out = append(out, o.Title, o.Subtitle)
		}
	}
	return strings.Join(out, "\n")
}

// stepButton is the visible button on the step with text, or fails.
func (tw *testWindow) stepButton(t *testing.T, text string) *widget.Button {
	t.Helper()
	for _, o := range tw.stepObjects() {
		if b, ok := o.(*widget.Button); ok && b.Visible() && b.Text == text {
			return b
		}
	}
	t.Fatalf("no [%s] button on step %v:\n%s", text, tw.onboarding.step, tw.stepText())
	return nil
}

func (tw *testWindow) onboardingCard(t *testing.T, id string) *onboardingCard {
	t.Helper()
	for _, c := range tw.onboarding.cards {
		if c.model.ID == id {
			return c
		}
	}
	t.Fatalf("no card for %s on the model step", id)
	return nil
}

func (tw *testWindow) step(t *testing.T) OnboardingStep {
	t.Helper()
	step, on := tw.Onboarding()
	if !on {
		t.Fatal("onboarding is not on screen")
	}
	return step
}

// A new user goes through every step: the permissions update live, nothing
// is chosen until they download a model, which they need to go on, and the
// last step reports that dictation reached its field.
func TestOnboardingWalkthrough(t *testing.T) {
	tw := newOnboardingWindow(t)
	tw.showOnboarding(StepWelcome)
	if tw.Window().Title() != OnboardingTitle || !tw.Visible() {
		t.Fatalf("window %q, visible %v", tw.Window().Title(), tw.Visible())
	}
	if text := tw.stepText(); !strings.Contains(text, "Step 1 of 4") || !strings.Contains(text, "Welcome to Goscriber") {
		t.Errorf("welcome step:\n%s", text)
	}

	test.Tap(tw.stepButton(t, LabelGetStarted))
	if tw.step(t) != StepPermissions {
		t.Fatalf("step %v after Get started", tw.step(t))
	}
	allowed := 0
	tw.SetPermissions([]PermissionRow{
		{Title: "Microphone", Status: "not asked yet", ActionLabel: "Allow…", OnOpen: func() { allowed++ }},
		{Title: "Accessibility", Status: "granted", OK: true},
	})
	if text := tw.stepText(); !strings.Contains(text, "✗ Microphone: not asked yet") || !strings.Contains(text, "✓ Accessibility: granted") {
		t.Errorf("permissions step:\n%s", text)
	}
	test.Tap(tw.stepButton(t, "Allow…"))
	if allowed != 1 {
		t.Errorf("Allow ran %d times", allowed)
	}
	tw.SetPermissions([]PermissionRow{{Title: "Microphone", Status: "granted", OK: true}})
	if text := tw.stepText(); !strings.Contains(text, "✓ Microphone: granted") {
		t.Errorf("the step did not follow the permission:\n%s", text)
	}

	test.Tap(tw.stepButton(t, LabelContinue))
	if tw.step(t) != StepModel {
		t.Fatalf("step %v after the permissions", tw.step(t))
	}
	text := tw.stepText()
	for _, want := range []string{
		"Nemotron Streaming 3.5", "Shows words as you speak · 32 languages", "742 MB download",
		"Parakeet TDT 0.6B v2", "Transcribes when you stop · English only", "700 MB download",
		OnboardingNeedModel,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%q missing from the model step:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Recommended") || strings.Contains(text, "✓") {
		t.Errorf("a model looks chosen before any download:\n%s", text)
	}
	if !tw.stepButton(t, LabelContinue).Disabled() || tw.store.Get().SelectedModel != "" {
		t.Fatal("the user must download a model to continue")
	}

	gate := make(chan struct{})
	tw.files.setGate(gate)
	c := tw.onboardingCard(t, parakeetV2)
	test.Tap(c.download)
	tw.q.until(t, "download progress", func() bool { return strings.HasPrefix(c.status.Text, "Downloading… 25 %") })
	if !c.progress.Visible() || c.download.Text != LabelCancelDownload {
		t.Errorf("while downloading: progress %v, button %q", c.progress.Visible(), c.download.Text)
	}
	if other := tw.onboardingCard(t, nemotron); !other.download.Disabled() {
		t.Error("a second download can start while one is in flight")
	}
	close(gate)
	tw.q.until(t, "the model to be selected", func() bool { return tw.store.Get().SelectedModel == parakeetV2 })
	tw.q.drain()
	if s := tw.store.Get(); s.ModelQuant != "Q8_0" {
		t.Errorf("downloaded %s, want the default Q8_0", s.ModelQuant)
	}
	if c.status.Text != "✓ Downloaded · selected" || c.download.Visible() {
		t.Errorf("once downloaded: %q, download button shown %v", c.status.Text, c.download.Visible())
	}
	if tw.stepButton(t, LabelContinue).Disabled() || strings.Contains(tw.stepText(), OnboardingNeedModel) {
		t.Error("Continue should be enabled once the model is downloaded")
	}

	test.Tap(tw.stepButton(t, LabelContinue))
	if tw.step(t) != StepTry {
		t.Fatalf("step %v after the model", tw.step(t))
	}
	text = tw.stepText()
	for _, want := range []string{"hold the shortcut, say a sentence, and let go", ShortcutLabel(config.Defaults().TranscribeShortcut)} {
		if !strings.Contains(text, want) {
			t.Errorf("%q missing from the try step:\n%s", want, text)
		}
	}
	if strings.Contains(text, "No model is downloaded yet") {
		t.Error("the try step warns of a missing model when one is downloaded")
	}
	if strings.Contains(text, LabelSkipSetup) || strings.Contains(text, TryWorked) {
		t.Errorf("try step before dictating:\n%s", text)
	}
	if tw.Window().Canvas().Focused() != tw.onboarding.tryEntry {
		t.Error("the field should have the keyboard, so the paste lands in it")
	}
	tw.onboarding.tryEntry.SetText("Hello from Goscriber.")
	if !strings.Contains(tw.stepText(), TryWorked) {
		t.Error("the step should say dictation worked once text arrives")
	}

	test.Tap(tw.stepButton(t, LabelDone))
	if _, on := tw.Onboarding(); on || tw.Page() != PageGeneral || tw.Window().Title() != SettingsTitle {
		t.Errorf("after Done: onboarding %v, page %v, title %q", on, tw.Page(), tw.Window().Title())
	}
	if want := []OnboardingStep{StepWelcome, StepPermissions, StepModel, StepTry}; !slices.Equal(tw.steps, want) {
		t.Errorf("steps reported %v, want %v", tw.steps, want)
	}
	if !slices.Equal(tw.done, []bool{false}) {
		t.Errorf("done reported %v", tw.done)
	}
}

// Skipping leaves the app with no model and nothing downloaded.
func TestOnboardingSkip(t *testing.T) {
	tw := newOnboardingWindow(t)
	tw.showOnboarding(StepWelcome)
	test.Tap(tw.stepButton(t, LabelSkipSetup))
	if _, on := tw.Onboarding(); on || tw.Page() != PageGeneral {
		t.Errorf("after Skip: onboarding %v, page %v", on, tw.Page())
	}
	if !slices.Equal(tw.done, []bool{true}) {
		t.Errorf("done reported %v", tw.done)
	}
	if s := tw.store.Get(); s.SelectedModel != "" {
		t.Errorf("skipping selected %s", s.SelectedModel)
	}
	for _, st := range tw.files.List() {
		if st.Done > 0 {
			t.Errorf("skipping downloaded %s", st.File.Name)
		}
	}
}

// Back returns to the step before; Show leaves onboarding for the pages
// without finishing it; About's button starts it again.
func TestOnboardingNavigation(t *testing.T) {
	tw := newOnboardingWindow(t)
	tw.showOnboarding(StepModel)
	test.Tap(tw.stepButton(t, LabelBack))
	if tw.step(t) != StepPermissions {
		t.Errorf("Back went to %v", tw.step(t))
	}
	test.Tap(tw.stepButton(t, LabelBack))
	if tw.step(t) != StepWelcome {
		t.Errorf("Back went to %v", tw.step(t))
	}
	if text := tw.stepText(); strings.Contains(text, "["+LabelBack+"]") {
		t.Errorf("the first step has no Back:\n%s", text)
	}

	tw.show(PageModels)
	if _, on := tw.Onboarding(); on || tw.Page() != PageModels {
		t.Errorf("Show left onboarding %v on page %v", on, tw.Page())
	}
	if len(tw.done) != 0 {
		t.Error("leaving onboarding for the pages does not finish it")
	}

	tw.selectPage(PageAbout)
	test.Tap(tw.buttons(PageAbout)[LabelRunSetup])
	if tw.step(t) != StepWelcome {
		t.Errorf("Run setup again opened %v", tw.step(t))
	}
}

// A model already downloaded is offered for use rather than for download,
// and a step out of range opens the welcome.
func TestOnboardingModelAlreadyDownloaded(t *testing.T) {
	tw := newOnboardingWindow(t)
	tw.files.setReady(parakeetV2, "F16")
	tw.showOnboarding(StepModel)
	c := tw.onboardingCard(t, parakeetV2)
	if c.status.Text != "Downloaded" || c.download.Visible() || !c.use.Visible() {
		t.Fatalf("status %q, download %v, use %v", c.status.Text, c.download.Visible(), c.use.Visible())
	}
	test.Tap(c.use)
	if s := tw.store.Get(); s.SelectedModel != parakeetV2 || s.ModelQuant != "F16" {
		t.Errorf("Use selected %s %s, want the downloaded F16", s.SelectedModel, s.ModelQuant)
	}
	if c.status.Text != "✓ Downloaded · selected" || c.use.Visible() {
		t.Errorf("after Use: %q, use shown %v", c.status.Text, c.use.Visible())
	}

	tw.showOnboarding(stepCount)
	if tw.step(t) != StepWelcome {
		t.Errorf("an unknown step opened %v", tw.step(t))
	}
}

// The try step follows the shortcut and activation mode, and warns when no
// model is downloaded.
func TestOnboardingTryStep(t *testing.T) {
	tw := newOnboardingWindow(t)
	if err := tw.store.Update(func(s *config.Settings) {
		s.TranscribeShortcut, s.ActivationMode = "ctrl+shift+d", config.ActivationToggle
	}); err != nil {
		t.Fatal(err)
	}
	tw.SetSettings(tw.store.Get())
	tw.showOnboarding(StepTry)
	text := tw.stepText()
	for _, want := range []string{"press the shortcut, say a sentence, and press it again", ShortcutLabel("ctrl+shift+d"), "No model is downloaded yet"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q missing from the try step:\n%s", want, text)
		}
	}
}

func TestOnboardingStepNames(t *testing.T) {
	for s := StepWelcome; s < stepCount; s++ {
		if got, ok := ParseOnboardingStep(s.String()); !ok || got != s {
			t.Errorf("%v: parsed %v, %v", s, got, ok)
		}
	}
	if _, ok := ParseOnboardingStep("finished"); ok {
		t.Error("an unknown name parsed")
	}
}
