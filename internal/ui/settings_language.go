package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/models"
)

// LanguageOption is a language the user can pick: its code, such as
// "es-ES", and its readable name.
type LanguageOption struct {
	Code, Name string
}

// LanguageSetting is what the Models page's language picker shows.
type LanguageSetting struct {
	// Options are the languages to pick from. None hides the picker.
	Options []LanguageOption
	// Selected is the code of the language picked.
	Selected string
	// Locked shows the language without letting it change: the model's
	// language is fixed. Note says why.
	Locked bool
	Note   string
}

// languageSetting is what the language picker shows for the selected model:
// the languages it takes, with the setting picked (or automatic detection
// when the setting is not one of them), or its fixed language, locked. With
// no model in the catalog selected there is nothing to pick.
func languageSetting(s config.Settings, cat *models.Catalog) LanguageSetting {
	m, ok := cat.Model(s.SelectedModel)
	if !ok || m.Kind != models.KindASR {
		return LanguageSetting{}
	}
	if choices := m.LanguageChoices(); choices != nil {
		l := LanguageSetting{Selected: m.ResolveLanguage(s.Language)}
		for _, c := range choices {
			l.Options = append(l.Options, LanguageOption{Code: c, Name: models.LanguageName(c)})
		}
		return l
	}
	fixed := m.FixedLanguage()
	l := LanguageSetting{
		Options:  []LanguageOption{{Code: fixed, Name: models.LanguageName(fixed)}},
		Selected: fixed,
		Locked:   true,
	}
	if fixed == models.LanguageAuto {
		l.Note = m.Name + " detects the language itself, from the audio."
	} else {
		l.Note = m.Name + " transcribes " + models.LanguageName(fixed) + " only."
	}
	return l
}

// Language is what the language picker shows, or will once the window
// opens.
func (w *SettingsWindow) Language() LanguageSetting { return languageSetting(w.cur, w.catalog()) }

// Detected is the readable name of the language the last dictation was
// detected in, or "".
func (w *SettingsWindow) Detected() string { return w.detected }

// SetDetected shows the language the last dictation was detected in, by its
// readable name; "" shows none.
func (w *SettingsWindow) SetDetected(name string) {
	w.detected = name
	w.refreshLanguage()
}

// PickLanguage picks a language by code as the user would in the picker.
func (w *SettingsWindow) PickLanguage(code string) {
	for _, o := range w.language.Options {
		if o.Code == code && w.languageSelect != nil && !w.language.Locked {
			w.languageSelect.SetSelected(o.Name)
		}
	}
}

// languageView is the language picker, bound to the language setting. What
// it offers depends on the selected model.
func (w *SettingsWindow) languageView() fyne.CanvasObject {
	w.languageSelect = widget.NewSelect(nil, w.languagePicked)
	w.languageNote = paragraph("")
	w.detectedLabel = paragraph("")
	w.languageBox = container.NewVBox(
		w.row("Language", "", w.languageSelect),
		w.languageNote, w.detectedLabel)
	w.bind("language", anyField(func(s *config.Settings) *string { return &s.Language }), w.languageSelect,
		func(s config.Settings) {
			w.language = languageSetting(s, w.catalog())
			w.refreshLanguage()
		})
	return w.languageBox
}

// refreshLanguage brings the picker in line with the language setting.
func (w *SettingsWindow) refreshLanguage() {
	if w.languageBox == nil {
		return
	}
	loading := w.loading
	w.loading = true
	defer func() { w.loading = loading }()

	l := w.language
	if len(l.Options) == 0 {
		w.languageBox.Hide()
		return
	}
	names := make([]string, len(l.Options))
	selected := ""
	for i, o := range l.Options {
		names[i] = o.Name
		if o.Code == l.Selected {
			selected = o.Name
		}
	}
	w.languageSelect.SetOptions(names)
	w.languageSelect.SetSelected(selected)
	if l.Locked {
		w.languageSelect.Disable()
	} else {
		w.languageSelect.Enable()
	}
	w.languageNote.SetText(l.Note)
	w.languageNote.Hidden = l.Note == ""
	w.detectedLabel.SetText("Detected in the last dictation: " + w.detected)
	w.detectedLabel.Hidden = w.detected == ""
	w.languageBox.Show()
	w.languageBox.Refresh()
}

func (w *SettingsWindow) languagePicked(name string) {
	if w.loading || w.language.Locked {
		return
	}
	for _, o := range w.language.Options {
		if o.Name == name && o.Code != w.language.Selected {
			w.language.Selected = o.Code
			setField(w, func(s *config.Settings) *string { return &s.Language }, o.Code)
		}
	}
}
