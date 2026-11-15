//go:build cgo && nemo

package nemo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/pipeline"
	"github.com/opragmatico/goscriber/internal/text"
)

// rareName is the custom word of the Queequeg fixture.
const rareName = "Queequeg"

// customWords are the custom words of these tests: the fixture's rare name
// among others it does not hold, some of which share its first sounds, so
// boosting them must not make the model write them.
var customWords = []string{rareName, "Kowalczyk", "Goscriber", "NVIDIA", "Nemotron", "Parakeet", "kubectl", "Ishmael", "Starbuck", "Ahab"}

// spellsRareName reports whether s spells the fixture's rare name right,
// and nothing else from customWords crept in.
func spellsRareName(t *testing.T, s string) bool {
	t.Helper()
	for _, w := range customWords[1:] {
		if strings.Contains(s, w) {
			t.Errorf("%q holds the custom word %q, which was never said", s, w)
		}
	}
	return strings.Contains(s, rareName+"'s")
}

// Nemotron misspells the fixture's rare name on its own and spells it right
// once asked to favour it (word boosting, at the default custom_word_boost),
// whole, streamed and in a live dictation, with the rest of the transcript
// unchanged.
func TestNemotronBoostsCustomWords(t *testing.T) {
	samples, rate, want := queequegFixture(t)
	e, _ := loadNemotron(t, false)
	boost := float32(config.Defaults().CustomWordBoost)

	plain, err := e.Recognize(context.Background(), samples, rate, engine.Options{Language: "en-US"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("without custom words: %q", plain.Text)
	if strings.Contains(plain.Text, rareName) {
		t.Errorf("Nemotron spelled %q without being asked to; the fixture no longer shows boosting at work", rareName)
	}

	opts := engine.Options{Language: "en-US", Phrases: customWords, Boost: boost}
	boosted, err := e.Recognize(context.Background(), samples, rate, opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.InterimResults = true
	streamed := streamClip(t, e, samples, rate, 20*time.Millisecond, 0, opts).final
	for _, r := range []struct {
		how  string
		text string
	}{{"whole", boosted.Text}, {"streamed", streamed.Text}} {
		t.Logf("boosted, %s: %q", r.how, r.text)
		if !spellsRareName(t, r.text) {
			t.Errorf("boosted, %s: %q does not spell %q", r.how, r.text, rareName)
		}
		if w := wer(want, r.text); w > 0 {
			t.Errorf("boosted, %s: WER %.1f%%: %q", r.how, 100*w, r.text)
		}
	}

	settings := config.Defaults()
	settings.CustomWords = customWords
	spec := engine.LoadSpec{ID: "nemotron-3.5", Path: testModel(t, nemotronFile)}
	d := dictateWith(t, e, pipeline.Model{Spec: spec, Name: "Nemotron", Language: "en-US", WordBoosting: true}, settings, samples, rate, 4)
	t.Logf("dictated: %q", d.text)
	if !spellsRareName(t, d.text) {
		t.Errorf("dictated: %q does not spell %q", d.text, rareName)
	}
}

// Parakeet TDT misspells the rare name and ignores a request to favour it:
// NeMo-Speech.cpp boosts words for RNNT decoders only. The cleanup stage's
// correction spells it right, in a live, chunked dictation too.
func TestParakeetCorrectsCustomWords(t *testing.T) {
	path := testModel(t, "parakeet-tdt-0.6b-v2.q8_0.gguf")
	samples, rate, want := queequegFixture(t)
	e := New()
	defer func() { _ = e.Unload() }()
	spec := engine.LoadSpec{ID: "parakeet", Path: path}
	if _, err := e.Load(context.Background(), spec); err != nil {
		t.Fatal(err)
	}

	plain, err := e.Recognize(context.Background(), samples, rate, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	asked, err := e.Recognize(context.Background(), samples, rate, engine.Options{Phrases: customWords, Boost: config.MaxCustomWordBoost})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("without custom words: %q", plain.Text)
	if strings.Contains(plain.Text, rareName) {
		t.Errorf("Parakeet spelled %q on its own; the fixture no longer shows correction at work", rareName)
	}
	if asked.Text != plain.Text {
		t.Errorf("asked to favour the custom words, Parakeet wrote %q instead of %q; it boosts words now", asked.Text, plain.Text)
	}

	threshold := config.Defaults().WordCorrectionThreshold
	corrected := text.CorrectWords(plain.Text, customWords, threshold)
	t.Logf("corrected: %q", corrected)
	if !spellsRareName(t, corrected) {
		t.Errorf("corrected: %q does not spell %q", corrected, rareName)
	}
	if w := wer(want, corrected); w > 0 {
		t.Errorf("corrected: WER %.1f%%: %q", 100*w, corrected)
	}

	settings := config.Defaults()
	settings.CustomWords = customWords
	d := dictateWith(t, e, pipeline.Model{Spec: spec, Name: "Parakeet"}, settings, samples, rate, 4)
	t.Logf("dictated: %q", d.text)
	if !spellsRareName(t, d.text) {
		t.Errorf("dictated: %q does not spell %q", d.text, rareName)
	}
}
