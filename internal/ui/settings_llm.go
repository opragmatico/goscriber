package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/llm"
)

// LLMActions back the Post-processing page, in the background. A nil
// action leaves its control out.
type LLMActions struct {
	// Models lists the models the provider at baseURL serves, sending the
	// key stored for it.
	Models func(ctx context.Context, baseURL string) ([]string, error)
	// HasKey reports whether an API key is stored for baseURL.
	HasKey func(baseURL string) (bool, error)
	// SetKey stores the API key for baseURL in the keychain; "" removes it.
	SetKey func(baseURL, key string) error
	// Try has the model the settings s name clean llm.SampleTranscript
	// and returns its answer.
	Try func(ctx context.Context, s config.Settings) (string, error)
	// Memory is the computer's memory, which picks the suggested model; 0
	// when it is not known.
	Memory uint64
}

// Labels and texts of the Post-processing page that tests look for.
const (
	LabelOtherProvider  = "Other"
	LabelSaveKey        = "Save"
	LabelRemoveKey      = "Remove"
	LabelRefreshModels  = "Refresh"
	LabelBuiltInPrompt  = "Use the built-in prompt"
	LabelTryLLM         = "Try it"
	llmKeyStoredHint    = "A key is stored for this provider. Type another to replace it."
	llmNoKeyHint        = "No key is stored for this provider."
	llmKeyUnknownHint   = "The keychain could not be read."
	llmPromptBuiltIn    = "The built-in prompt. Edit it to make it your own."
	llmPromptCustom     = "Your own prompt."
	llmPromptEmpty      = "Empty, so the built-in prompt is used."
	llmPromptPlaceholde = "Where the transcript goes, write " + llm.Placeholder + "; without it, the transcript follows the prompt."
)

// llmState is what the Post-processing page knows beyond the settings.
type llmState struct {
	models     *widget.SelectEntry
	modelsNote *widget.Label
	listedFor  string // the base URL the models were listed for, or are being
	listing    bool
	listNote   string // what the last listing found
	noteFor    string // the base URL it listed

	key     *widget.Entry
	keyNote *widget.Label
	remove  *widget.Button
	keyFor  string // the base URL keyNote is about

	try     *widget.Button
	tryNote *widget.Label
	trying  bool
}

func (w *SettingsWindow) postProcessingPage() fyne.CanvasObject {
	rows := []fyne.CanvasObject{
		w.row("Clean up transcripts", "A language model fixes misheard words, numbers and punctuation before the text is pasted, "+
			"and in the files you transcribe. It adds a moment to each dictation; if it fails, the text is pasted as transcribed.",
			boolSetting(w, "llm_enabled", func(s *config.Settings) *bool { return &s.LLMEnabled })),
		w.row("Provider", "Any server with an OpenAI-compatible API: on this computer, or a service that takes an API key.", w.providerSetting()),
		w.row("Base URL", "Its API's address, ending in /v1 for most.", w.baseURLSetting()),
	}
	if a := w.opts.LLM; a.SetKey != nil {
		rows = append(rows, w.row("API key", "Kept in the system keychain, not in the settings file. Providers on this computer need none.", w.apiKeySetting()))
	}
	rows = append(rows,
		w.row("Model", "", w.llmModelSetting()),
		w.row("Prompt", llmPromptPlaceholde, w.promptSetting()),
	)
	if w.opts.LLM.Try != nil {
		rows = append(rows, w.row("Test", "Has the model clean a sample dictation with these settings.", w.tryLLMSetting()))
	}
	return settingsPage("Post-processing", group("Language model", rows...))
}

// providerSetting picks a known provider, which sets the base URL; a base
// URL of no provider shows as Other.
func (w *SettingsWindow) providerSetting() *widget.Select {
	field := func(s *config.Settings) *string { return &s.LLMBaseURL }
	labels := make([]string, 0, len(llm.Providers)+1)
	for _, p := range llm.Providers {
		labels = append(labels, p.Name)
	}
	labels = append(labels, LabelOtherProvider)
	sel := widget.NewSelect(labels, nil)
	sel.OnChanged = func(name string) {
		for _, p := range llm.Providers {
			if p.Name == name {
				setField(w, field, p.BaseURL)
			}
		}
	}
	w.bind("llm_base_url", anyField(field), sel, func(s config.Settings) {
		if p, ok := llm.ProviderFor(s.LLMBaseURL); ok {
			sel.SetSelected(p.Name)
		} else {
			sel.SetSelected(LabelOtherProvider)
		}
	})
	return sel
}

// baseURLSetting is the base URL, saved as it is typed once it is an http
// or https URL.
func (w *SettingsWindow) baseURLSetting() *widget.Entry {
	field := func(s *config.Settings) *string { return &s.LLMBaseURL }
	e := widget.NewEntry()
	e.Validator = func(text string) error {
		_, err := llm.ParseBaseURL(text)
		return err
	}
	e.OnChanged = func(text string) {
		if _, err := llm.ParseBaseURL(text); err == nil {
			setField(w, field, strings.TrimSpace(text))
		}
	}
	w.bind("llm_base_url", anyField(field), e, func(s config.Settings) {
		if strings.TrimSpace(e.Text) != s.LLMBaseURL {
			e.SetText(s.LLMBaseURL)
		}
		w.baseURLShown(s.LLMBaseURL)
	})
	return e
}

// baseURLShown follows a base URL the page shows: whether a key is stored
// for it, and the models it serves.
func (w *SettingsWindow) baseURLShown(baseURL string) {
	if !w.created {
		return
	}
	w.refreshKey(baseURL)
	if w.visible {
		w.refreshLLMModels(false)
	}
}

// apiKeySetting stores an API key for the base URL in the keychain. The
// stored key is never shown: the field takes a new one.
func (w *SettingsWindow) apiKeySetting() fyne.CanvasObject {
	st := &w.llm
	st.key = widget.NewPasswordEntry()
	st.key.SetPlaceHolder("API key")
	st.keyNote = note("")
	save := func() {
		key := strings.TrimSpace(st.key.Text)
		if key == "" {
			return
		}
		w.storeKey(w.cur.LLMBaseURL, key)
	}
	st.key.OnSubmitted = func(string) { save() }
	saveButton := widget.NewButton(LabelSaveKey, save)
	st.remove = widget.NewButtonWithIcon(LabelRemoveKey, theme.DeleteIcon(), func() { w.storeKey(w.cur.LLMBaseURL, "") })
	return container.NewVBox(
		container.NewBorder(nil, nil, nil, container.NewHBox(saveButton, st.remove), st.key),
		st.keyNote,
	)
}

// storeKey stores key for baseURL, or removes it with "", in the
// background.
func (w *SettingsWindow) storeKey(baseURL, key string) {
	set := w.opts.LLM.SetKey
	go func() {
		err := set(baseURL, key)
		w.do(func() {
			if err != nil {
				w.log().Warn("the API key could not be stored", "err", err)
				w.llm.keyNote.SetText(fmt.Sprintf("Could not store the key: %v", err))
				return
			}
			w.llm.key.SetText("")
			w.llm.keyFor = ""
			w.refreshKey(baseURL)
		})
	}()
}

// refreshKey says whether a key is stored for baseURL, reading the
// keychain in the background.
func (w *SettingsWindow) refreshKey(baseURL string) {
	st := &w.llm
	has := w.opts.LLM.HasKey
	if st.keyNote == nil || has == nil || st.keyFor == baseURL {
		return
	}
	st.keyFor = baseURL
	go func() {
		stored, err := has(baseURL)
		w.do(func() {
			if st.keyFor != baseURL {
				return
			}
			switch {
			case err != nil:
				w.log().Warn("the keychain could not be read", "err", err)
				st.keyNote.SetText(llmKeyUnknownHint)
			case stored:
				st.keyNote.SetText(llmKeyStoredHint)
			default:
				st.keyNote.SetText(llmNoKeyHint)
			}
			if stored {
				st.remove.Enable()
			} else {
				st.remove.Disable()
			}
		})
	}()
}

// llmModelSetting is the model: typed, or picked from those the provider
// serves. Empty is the suggested model, for a provider on this computer.
func (w *SettingsWindow) llmModelSetting() fyne.CanvasObject {
	field := func(s *config.Settings) *string { return &s.LLMModel }
	st := &w.llm
	st.models = widget.NewSelectEntry(nil)
	st.models.OnChanged = func(text string) { setField(w, field, strings.TrimSpace(text)) }
	st.modelsNote = note("")
	w.bind("llm_model", anyField(field), st.models, func(s config.Settings) {
		if strings.TrimSpace(st.models.Text) != s.LLMModel {
			st.models.SetText(s.LLMModel)
		}
		st.models.SetPlaceHolder(w.modelPlaceholder(s))
		w.showModelsNote()
	})
	if w.opts.LLM.Models == nil {
		return container.NewVBox(st.models, st.modelsNote)
	}
	refresh := widget.NewButtonWithIcon(LabelRefreshModels, theme.ViewRefreshIcon(), func() { w.refreshLLMModels(true) })
	return container.NewVBox(container.NewBorder(nil, nil, nil, refresh, st.models), st.modelsNote)
}

// modelPlaceholder says what an empty model means for the settings s.
func (w *SettingsWindow) modelPlaceholder(s config.Settings) string {
	if llm.IsLocal(s.LLMBaseURL) {
		return llm.SuggestedModel(w.opts.LLM.Memory)
	}
	return "Choose a model"
}

// suggestionHint explains the model suggested for this computer.
func (w *SettingsWindow) suggestionHint() string {
	suggested := llm.SuggestedModel(w.opts.LLM.Memory)
	why := "for computers with less than 32 GB of memory"
	if mem := w.opts.LLM.Memory; mem > 0 {
		why = "for this computer's " + llm.FormatMemory(mem) + " of memory"
	}
	return fmt.Sprintf("Left empty, %s, suggested %s. With Ollama, install it with: ollama pull %s", suggested, why, suggested)
}

// refreshLLMModels lists the models the provider serves, in the
// background: once per base URL, unless again. A listing that failed is
// tried again the next time.
func (w *SettingsWindow) refreshLLMModels(again bool) {
	st := &w.llm
	list := w.opts.LLM.Models
	baseURL := w.cur.LLMBaseURL
	if list == nil || st.models == nil || st.listing || (!again && st.listedFor == baseURL) {
		return
	}
	st.listing, st.listedFor = true, baseURL
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		models, err := list(ctx, baseURL)
		cancel()
		w.do(func() {
			st.listing = false
			if w.cur.LLMBaseURL != baseURL {
				w.refreshLLMModels(false)
				return
			}
			st.models.SetOptions(models)
			st.noteFor = baseURL
			switch {
			case err != nil:
				st.listedFor = ""
				st.listNote = fmt.Sprintf("Could not list the models of %s: %v", baseURL, err)
			case len(models) == 0:
				st.listNote = "The provider serves no models yet."
			default:
				st.listNote = "The provider serves: " + strings.Join(models, ", ") + "."
			}
			w.showModelsNote()
		})
	}()
}

// showModelsNote explains the model setting: what an empty one means, and
// what the provider serves or why it could not be asked.
func (w *SettingsWindow) showModelsNote() {
	st := &w.llm
	var lines []string
	if w.cur.LLMModel == "" && llm.IsLocal(w.cur.LLMBaseURL) {
		lines = append(lines, w.suggestionHint())
	}
	if st.listNote != "" && st.noteFor == w.cur.LLMBaseURL {
		lines = append(lines, st.listNote)
	}
	st.modelsNote.SetText(strings.TrimSpace(strings.Join(lines, "\n")))
}

// promptSetting edits the prompt. An empty llm_prompt is the built-in
// prompt, which the field shows; editing it makes it the user's own, and
// the button puts the built-in one back.
func (w *SettingsWindow) promptSetting() fyne.CanvasObject {
	field := func(s *config.Settings) *string { return &s.LLMPrompt }
	e := widget.NewMultiLineEntry()
	e.Wrapping = fyne.TextWrapWord
	e.SetMinRowsVisible(8)
	which := note("")
	// cleared: the user emptied the field, which saves the built-in prompt
	// but leaves the field empty for them to write their own.
	cleared := false
	builtIn := widget.NewButton(LabelBuiltInPrompt, func() {
		cleared = false
		if w.cur.LLMPrompt == "" {
			w.show(w.cur) // the field was emptied: show the prompt again
			return
		}
		setField(w, field, "")
	})
	e.OnChanged = func(text string) {
		if w.loading {
			return
		}
		cleared = strings.TrimSpace(text) == ""
		if cleared || text == llm.DefaultPrompt {
			text = ""
		}
		setField(w, field, text)
	}
	w.bind("llm_prompt", anyField(field), e, func(s config.Settings) {
		shown := s.LLMPrompt
		if shown == "" && !cleared {
			shown = llm.DefaultPrompt
		}
		if e.Text != shown {
			e.SetText(shown)
		}
		switch {
		case s.LLMPrompt != "":
			which.SetText(llmPromptCustom)
			builtIn.Enable()
		case cleared:
			which.SetText(llmPromptEmpty)
			builtIn.Enable()
		default:
			which.SetText(llmPromptBuiltIn)
			builtIn.Disable()
		}
	})
	return container.NewVBox(e, container.NewBorder(nil, nil, nil, builtIn, which))
}

// tryLLMSetting has the model clean a sample dictation and shows what it
// wrote, or why it failed.
func (w *SettingsWindow) tryLLMSetting() fyne.CanvasObject {
	st := &w.llm
	st.tryNote = note("")
	st.tryNote.Hide()
	st.try = widget.NewButtonWithIcon(LabelTryLLM, theme.MediaPlayIcon(), w.tryLLM)
	return container.NewVBox(container.NewHBox(st.try), st.tryNote)
}

func (w *SettingsWindow) tryLLM() {
	st := &w.llm
	if st.trying {
		return
	}
	st.trying = true
	st.try.Disable()
	st.tryNote.SetText("Sent: “" + llm.SampleTranscript + "”…")
	st.tryNote.Show()
	s := w.cur.Clone()
	try := w.opts.LLM.Try
	go func() {
		start := time.Now()
		answer, err := try(context.Background(), s)
		took := time.Since(start)
		w.do(func() {
			st.trying = false
			st.try.Enable()
			if err != nil {
				st.tryNote.SetText(fmt.Sprintf("It failed after %.1f s: %v", took.Seconds(), err))
				return
			}
			st.tryNote.SetText(fmt.Sprintf("Sent: “%s”\nGot back, in %.1f s: “%s”", llm.SampleTranscript, took.Seconds(), answer))
		})
	}()
}
