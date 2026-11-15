package pipeline

import (
	"context"

	"github.com/opragmatico/goscriber/internal/text"
)

// CleanupStage is the cleanup text stage: it tidies the transcript with
// internal/text, following the settings the session started with, in this
// order:
//
//  1. Custom words the model was not asked to favour (SessionInfo.Boosted)
//     are corrected: near-misses of custom_words closer than
//     word_correction_threshold take their spelling. A model that boosted
//     them already spells them, and correcting its transcript again would
//     only add the chance of turning an ordinary word into a custom one.
//  2. With filler_word_removal, filler words go: custom_filler_words when
//     set, else the built-in ones for the transcript's language
//     (SessionInfo.TextLanguage), which are only those that are no word in
//     any language when it is not known.
//  3. Whitespace is collapsed and trimmed, and a transcript with no letter
//     or digit left becomes empty, so nothing is pasted.
//
// append_trailing_space is not applied here: the output adds the space
// when it delivers the text.
type CleanupStage struct{}

// Process implements TextStage.
func (CleanupStage) Process(_ context.Context, s string, info SessionInfo) (string, error) {
	set := info.Settings
	if !info.Boosted {
		s = text.CorrectWords(s, set.CustomWords, set.WordCorrectionThreshold)
	}
	if set.FillerWordRemoval {
		fillers := set.CustomFillerWords
		if fillers == nil {
			fillers = text.Fillers(info.TextLanguage)
		}
		s = text.RemoveFillers(s, fillers)
	}
	return text.Tidy(s), nil
}
