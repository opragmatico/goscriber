package pipeline

import (
	"context"

	"github.com/opragmatico/goscriber/internal/text"
)

// CleanupStage is the cleanup text stage: it tidies the transcript with
// internal/text, following the settings the session started with.
//
// Custom words the model was not asked to favour (SessionInfo.Boosted) are
// corrected: near-misses of custom_words closer than
// word_correction_threshold take their spelling. A model that boosted them
// already spells them, and correcting its transcript again would only add
// the chance of turning an ordinary word into a custom one.
type CleanupStage struct{}

// Process implements TextStage.
func (CleanupStage) Process(_ context.Context, s string, info SessionInfo) (string, error) {
	if !info.Boosted {
		s = text.CorrectWords(s, info.Settings.CustomWords, info.Settings.WordCorrectionThreshold)
	}
	return s, nil
}
