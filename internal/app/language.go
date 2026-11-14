package app

import (
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/models"
	"github.com/opragmatico/goscriber/internal/ui"
)

// languageSetting is what the language picker shows for the selected model:
// the languages it takes, with the setting picked (or automatic detection
// when the setting is not one of them), or its fixed language, locked. With
// no model in the catalog selected there is nothing to pick.
func languageSetting(s config.Settings) ui.LanguageSetting {
	m, ok := models.Default().Model(s.SelectedModel)
	if !ok || m.Kind != models.KindASR {
		return ui.LanguageSetting{}
	}
	if choices := m.LanguageChoices(); choices != nil {
		l := ui.LanguageSetting{Selected: m.ResolveLanguage(s.Language)}
		for _, c := range choices {
			l.Options = append(l.Options, ui.LanguageOption{Code: c, Name: models.LanguageName(c)})
		}
		return l
	}
	fixed := m.FixedLanguage()
	l := ui.LanguageSetting{
		Options:  []ui.LanguageOption{{Code: fixed, Name: models.LanguageName(fixed)}},
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

// pickLanguage saves the language the user picked, on the main thread. The
// next recording uses it.
func (sh *shell) pickLanguage(code string) {
	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.Language = code }); err != nil {
		sh.rt.Log.Warn("the language could not be saved", "err", err)
	}
}

// showDetected shows the language the last dictation was detected in, on
// the main thread.
func (sh *shell) showDetected(languages []string) {
	name := ""
	if len(languages) > 0 {
		name = models.LanguageName(languages[0])
	}
	sh.settings.SetDetected(name)
}
