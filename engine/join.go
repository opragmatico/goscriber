package engine

import (
	"slices"
	"strings"
	"time"
)

// JoinFinals merges finals, in order, into one final: their texts joined by
// spaces (empty ones left out), their words, and their languages in the
// order first seen. AudioProcessed is the furthest any of them reaches and
// Confidence their mean. A single final comes back as it is; none gives an
// empty final.
//
// A stream with endpointing yields one final per utterance, and a recording
// decoded in pieces one per piece. Word positions are kept as they are, so
// the finals of separately decoded pieces need their words moved to the
// piece's place in the audio first (see Result.Offset).
func JoinFinals(finals []Result) Result {
	if len(finals) == 1 {
		return finals[0]
	}
	out := Result{Final: true}
	var texts []string
	var conf float32
	for _, r := range finals {
		if t := strings.TrimSpace(r.Text); t != "" {
			texts = append(texts, t)
		}
		out.Words = append(out.Words, r.Words...)
		for _, l := range r.Languages {
			if !slices.Contains(out.Languages, l) {
				out.Languages = append(out.Languages, l)
			}
		}
		out.AudioProcessed = max(out.AudioProcessed, r.AudioProcessed)
		conf += r.Confidence
	}
	out.Text = strings.Join(texts, " ")
	if len(finals) > 0 {
		out.Confidence = conf / float32(len(finals))
	}
	return out
}

// Offset returns r as if its audio started d into a longer recording: its
// words and AudioProcessed move later by d. The words are copied.
func (r Result) Offset(d time.Duration) Result {
	if len(r.Words) > 0 {
		words := make([]Word, len(r.Words))
		for i, w := range r.Words {
			w.Start += d
			w.End += d
			words[i] = w
		}
		r.Words = words
	}
	r.AudioProcessed += d
	return r
}
