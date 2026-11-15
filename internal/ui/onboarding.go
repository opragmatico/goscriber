package ui

import (
	"fmt"
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/models"
)

// OnboardingStep is a step of first-run onboarding, in the order the user
// goes through them.
type OnboardingStep int

// Onboarding steps.
const (
	// StepWelcome says what Goscriber does and what setting up involves.
	StepWelcome OnboardingStep = iota
	// StepPermissions shows each macOS permission, live, with a button to
	// grant it.
	StepPermissions
	// StepModel offers the speech models for download. Nothing is chosen or
	// downloaded until the user presses Download.
	StepModel
	// StepTry has the user dictate into a field with the shortcut.
	StepTry
	stepCount
)

var (
	stepNames  = [stepCount]string{"welcome", "permissions", "model", "try"}
	stepTitles = [stepCount]string{"Welcome to Goscriber", "Permissions", "Choose a speech model", "Try it"}
)

// String returns the step's name, which the app saves to resume onboarding
// after a quit.
func (s OnboardingStep) String() string {
	if s < 0 || s >= stepCount {
		return fmt.Sprintf("OnboardingStep(%d)", int(s))
	}
	return stepNames[s]
}

// ParseOnboardingStep reads a step's name, as String writes it.
func ParseOnboardingStep(name string) (OnboardingStep, bool) {
	i := slices.Index(stepNames[:], name)
	return OnboardingStep(i), i >= 0
}

// OnboardingTitle is the window's title during onboarding.
const OnboardingTitle = "Set Up Goscriber"

// Labels and texts of onboarding that tests and the app look for.
const (
	LabelGetStarted = "Get started"
	LabelSkipSetup  = "Skip setup"
	LabelBack       = "Back"
	LabelContinue   = "Continue"
	LabelDone       = "Done"
	LabelRunSetup   = "Run setup again"
	// OnboardingNeedModel is the model step's note until the selected model
	// is downloaded.
	OnboardingNeedModel = "Download a model to continue, or skip setup and download one later from the tray."
	// TryWorked is what the last step says once text arrives in its field.
	TryWorked = "✓ It works. The shortcut dictates the same way into any app."
)

// onboardingState is what the window shows while onboarding is on screen,
// in place of the settings pages.
type onboardingState struct {
	active  bool
	step    OnboardingStep
	content fyne.CanvasObject // the step on screen

	// Widgets of the step on screen that follow the settings, the
	// permissions and the model files; nil on the other steps.
	perms      *fyne.Container
	cards      []*onboardingCard
	next       *widget.Button
	modelNote  *widget.Label
	tryHint    *widget.Label
	tryKey     *widget.Label
	tryNoModel *widget.Label
	tryEntry   *widget.Entry
	tryResult  *widget.Label
}

// onboardingCard is one model on the model step: what it does and its
// size, with a Download button and progress, and Use this model once it is
// downloaded and not the one selected.
type onboardingCard struct {
	model    *models.Model
	card     *widget.Card
	status   *widget.Label
	progress *widget.ProgressBar
	download *widget.Button
	use      *widget.Button
}

// ShowOnboarding opens the window on step of onboarding, in place of the
// settings pages, and brings it to the front. Show goes back to the pages;
// so do finishing and skipping onboarding, which then run OnOnboardingDone.
func (w *SettingsWindow) ShowOnboarding(step OnboardingStep) {
	if w.win == nil {
		w.create()
	}
	w.onboarding.active = true
	w.goStep(step)
	w.win.Show()
	w.win.RequestFocus()
	w.visible = true
	w.refreshModelFiles()
}

// Onboarding returns the step of onboarding on screen, and false while the
// window shows the settings pages.
func (w *SettingsWindow) Onboarding() (OnboardingStep, bool) {
	return w.onboarding.step, w.onboarding.active
}

// goStep puts step on screen and tells the app, so it can resume there.
func (w *SettingsWindow) goStep(step OnboardingStep) {
	if step < 0 || step >= stepCount {
		step = StepWelcome
	}
	w.onboarding = onboardingState{active: true, step: step}
	o := &w.onboarding
	switch step {
	case StepWelcome:
		o.content = w.welcomeStep()
	case StepPermissions:
		o.content = w.permissionsStep()
	case StepModel:
		o.content = w.modelStep()
	case StepTry:
		o.content = w.tryStep()
	}
	w.refreshOnboarding()
	w.layout()
	if o.tryEntry != nil {
		w.win.Canvas().Focus(o.tryEntry)
	}
	if w.opts.OnOnboardingStep != nil {
		w.opts.OnOnboardingStep(step)
	}
}

// leaveOnboarding puts the settings pages back on screen.
func (w *SettingsWindow) leaveOnboarding() {
	if !w.onboarding.active {
		return
	}
	w.onboarding = onboardingState{}
	w.layout()
}

// endOnboarding finishes onboarding, or skips the rest of it, and shows the
// General page.
func (w *SettingsWindow) endOnboarding(skipped bool) {
	w.leaveOnboarding()
	w.selectPage(PageGeneral)
	if w.opts.OnOnboardingDone != nil {
		w.opts.OnOnboardingDone(skipped)
	}
}

// stepView lays out a step: where it is in the setup and its title, its body
// in a scroll, and the buttons along the bottom: Skip setup and Back on the
// left, primary on the right.
func (w *SettingsWindow) stepView(step OnboardingStep, primary *widget.Button, body ...fyne.CanvasObject) fyne.CanvasObject {
	where := note(fmt.Sprintf("Step %d of %d", int(step)+1, int(stepCount)))
	heading := widget.NewLabelWithStyle(stepTitles[step], fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	heading.SizeName = theme.SizeNameSubHeadingText

	footer := container.NewHBox()
	if step != StepTry {
		footer.Add(widget.NewButton(LabelSkipSetup, func() { w.endOnboarding(true) }))
	}
	if step > StepWelcome {
		footer.Add(widget.NewButtonWithIcon(LabelBack, theme.NavigateBackIcon(), func() { w.goStep(step - 1) }))
	}
	primary.Importance = widget.HighImportance
	footer.Add(layout.NewSpacer())
	footer.Add(primary)

	top := container.NewVBox(append([]fyne.CanvasObject{where, heading}, body...)...)
	return container.NewBorder(nil, container.NewPadded(footer), nil, nil, container.NewVScroll(container.NewPadded(top)))
}

func (w *SettingsWindow) welcomeStep() fyne.CanvasObject {
	start := widget.NewButtonWithIcon(LabelGetStarted, theme.NavigateNextIcon(), func() { w.goStep(StepPermissions) })
	return w.stepView(StepWelcome, start,
		paragraph("Goscriber turns what you say into text in the app you are using: press a shortcut, speak, "+
			"and the transcript is pasted where you were typing. Speech recognition runs on this computer, "+
			"so your audio never leaves it."),
		paragraph("Setting up takes a couple of minutes:"),
		paragraph("1. Allow the permissions Goscriber needs.\n"+
			"2. Choose a speech model and download it.\n"+
			"3. Dictate your first sentence."),
		note("You can skip this now and run it again later from Settings, on the About page."),
	)
}

func (w *SettingsWindow) permissionsStep() fyne.CanvasObject {
	o := &w.onboarding
	o.perms = container.NewVBox()
	next := widget.NewButtonWithIcon(LabelContinue, theme.NavigateNextIcon(), func() { w.goStep(StepModel) })
	return w.stepView(StepPermissions, next,
		paragraph("Goscriber needs these to record your voice, hear the shortcut and paste the transcript. "+
			"Each status updates as soon as you change it in System Settings."),
		o.perms,
	)
}

func (w *SettingsWindow) modelStep() fyne.CanvasObject {
	o := &w.onboarding
	cards := container.NewVBox()
	for _, m := range w.catalog().ASR() {
		c := w.newOnboardingCard(m)
		o.cards = append(o.cards, c)
		cards.Add(c.card)
	}
	o.modelNote = note(OnboardingNeedModel)
	o.next = widget.NewButtonWithIcon(LabelContinue, theme.NavigateNextIcon(), func() { w.goStep(StepTry) })
	return w.stepView(StepModel, o.next,
		paragraph("Goscriber does not come with a speech model: choose the one that suits how you dictate. "+
			"You can download another or switch later in Settings, on the Models page."),
		cards,
		o.modelNote,
	)
}

func (w *SettingsWindow) newOnboardingCard(m *models.Model) *onboardingCard {
	c := &onboardingCard{model: m}
	c.status = widget.NewLabel("")
	c.status.Wrapping = fyne.TextWrapWord
	c.progress = widget.NewProgressBar()
	c.progress.Hide()
	c.download = widget.NewButtonWithIcon(LabelDownload, theme.DownloadIcon(), func() {
		if dl := w.models.downloads[m.ID]; dl != nil {
			dl.cancel()
			return
		}
		w.download(m.ID, w.onboardingQuant(m))
	})
	c.use = widget.NewButtonWithIcon(LabelUseModel, theme.ConfirmIcon(), func() {
		quant := w.onboardingQuant(m)
		if w.models.ready(m.ID, quant) {
			w.change(func(s *config.Settings) { s.SelectedModel, s.ModelQuant = m.ID, quant })
		}
	})
	f, _ := m.File(m.DefaultQuant)
	content := container.NewVBox(
		paragraph(m.Description),
		note(fmt.Sprintf("%s download · %s licence", models.FormatBytes(f.Size), m.License)),
		c.status, c.progress,
		container.NewHBox(c.download, layout.NewSpacer(), c.use),
	)
	c.card = widget.NewCard(m.Name, modelSubtitle(m, false), content)
	return c
}

// onboardingQuant is the quantisation the model step offers for m: the one
// downloading, else the one it runs in when selected, else its default,
// unless only another one is downloaded.
func (w *SettingsWindow) onboardingQuant(m *models.Model) string {
	if dl := w.models.downloads[m.ID]; dl != nil {
		return dl.quant
	}
	quant := m.DefaultQuant
	if w.cur.SelectedModel == m.ID {
		quant = usedQuant(m, w.cur)
	}
	if !w.models.ready(m.ID, quant) {
		for _, q := range m.Quants() {
			if w.models.ready(m.ID, q) {
				return q
			}
		}
	}
	return quant
}

func (w *SettingsWindow) tryStep() fyne.CanvasObject {
	o := &w.onboarding
	o.tryHint = paragraph("")
	o.tryKey = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	o.tryKey.SizeName = theme.SizeNameSubHeadingText
	o.tryNoModel = paragraph("No model is downloaded yet, so nothing can be transcribed: go back to download one, " +
		"or finish now and download one later.")
	o.tryNoModel.Importance = widget.WarningImportance
	o.tryEntry = widget.NewMultiLineEntry()
	o.tryEntry.SetPlaceHolder("What you say appears here.")
	o.tryEntry.SetMinRowsVisible(4)
	o.tryEntry.Wrapping = fyne.TextWrapWord
	o.tryResult = paragraph(TryWorked)
	o.tryResult.Importance = widget.SuccessImportance
	o.tryResult.Hide()
	o.tryEntry.OnChanged = func(text string) {
		if strings.TrimSpace(text) != "" {
			o.tryResult.Show()
		}
	}
	done := widget.NewButtonWithIcon(LabelDone, theme.ConfirmIcon(), func() { w.endOnboarding(false) })
	return w.stepView(StepTry, done, o.tryHint, o.tryKey, o.tryNoModel, o.tryEntry, o.tryResult)
}

// tryHint says how to dictate into the field with the activation mode; the
// shortcut is shown on its own line below it.
func tryHint(mode config.ActivationMode) string {
	how := "Click in the box below, then hold the shortcut, say a sentence, and let go. "
	if mode == config.ActivationToggle {
		how = "Click in the box below, then press the shortcut, say a sentence, and press it again. "
	}
	return how + "The transcript appears in the box, as it will in any app you dictate into."
}

// refreshOnboarding brings the step on screen in line with the settings, the
// permissions and the model files.
func (w *SettingsWindow) refreshOnboarding() {
	o := &w.onboarding
	if !o.active {
		return
	}
	if o.perms != nil {
		o.perms.Objects = w.permissionRows()
		o.perms.Refresh()
	}
	for _, c := range o.cards {
		w.refreshOnboardingCard(c)
	}
	ready := w.modelReady()
	if o.next != nil {
		if ready {
			o.next.Enable()
		} else {
			o.next.Disable()
		}
	}
	if o.modelNote != nil {
		o.modelNote.Hidden = ready
		o.modelNote.Refresh()
	}
	if o.tryHint != nil {
		o.tryHint.SetText(tryHint(w.cur.ActivationMode))
		o.tryKey.SetText(ShortcutLabel(w.cur.TranscribeShortcut))
		o.tryNoModel.Hidden = ready
	}
	// Widgets shown or hidden above need their containers laid out again.
	o.content.Refresh()
}

func (w *SettingsWindow) refreshOnboardingCard(c *onboardingCard) {
	id := c.model.ID
	quant := w.onboardingQuant(c.model)
	ready := w.models.ready(id, quant)
	selected := ready && w.cur.SelectedModel == id && strings.EqualFold(usedQuant(c.model, w.cur), quant)

	status := w.fileStatus(id, quant)
	if selected {
		status = "✓ " + status + " · selected"
	}
	c.status.SetText(status)
	w.syncDownload(id, quant, c.progress, c.download)
	if ready && !selected && w.models.downloads[id] == nil {
		c.use.Show()
	} else {
		c.use.Hide()
	}
}
