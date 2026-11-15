package ui

import (
	"fyne.io/fyne/v2"

	"github.com/opragmatico/goscriber/internal/config"
)

// HistoryPlaceholder is what the History page says until the transcription
// history (#32) fills it.
const HistoryPlaceholder = "Your recent dictations will be listed here, with their recordings."

func (w *SettingsWindow) historyPage() fyne.CanvasObject {
	return settingsPage("History",
		paragraph(HistoryPlaceholder),
		group("Keep",
			w.row("Dictations", "How many recent dictations to keep. 0 keeps none.",
				intEntrySetting(w, "history_limit", func(s *config.Settings) *int { return &s.HistoryLimit }, 0, 100000)),
			w.row("Recordings", "How long to keep the audio of each dictation.",
				choiceSetting(w, "recording_retention", func(s *config.Settings) *config.RecordingRetention { return &s.RecordingRetention },
					[]choice[config.RecordingRetention]{
						{config.RetentionPreserveLimit, "As long as the dictation is kept"},
						{config.RetentionDays3, "3 days"},
						{config.RetentionWeeks2, "2 weeks"},
						{config.RetentionMonths3, "3 months"},
						{config.RetentionNever, "Do not keep recordings"},
					}, nil)),
		),
	)
}
