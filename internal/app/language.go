package app

import (
	"github.com/opragmatico/goscriber/internal/models"
)

// showDetected shows the language the last dictation was detected in, on
// the main thread.
func (sh *shell) showDetected(languages []string) {
	name := ""
	if len(languages) > 0 {
		name = models.LanguageName(languages[0])
	}
	sh.settings.SetDetected(name)
}
