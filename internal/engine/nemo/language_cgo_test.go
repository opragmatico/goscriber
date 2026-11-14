//go:build cgo && nemo

package nemo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/engine"
)

// spokenSpanish reports whether langs holds a Spanish locale. The model
// tags the Spanish fixture es-US even when asked for es-ES: the tag is the
// language it heard, not the one it was asked for.
func spokenSpanish(langs []string) bool {
	for _, l := range langs {
		if strings.HasPrefix(l, "es-") {
			return true
		}
	}
	return false
}

// The Spanish fixture transcribes correctly asked for es-ES and with
// automatic detection, decoded whole and streamed: a stream needs no
// language up front, because Nemotron's "auto" is a prompt of its own and
// the model detects the language from the audio.
func TestNemotronSpanish(t *testing.T) {
	samples, rate, want := spanishFixture(t)
	e, _ := loadNemotron(t, false)
	for _, lang := range []string{"es-ES", "auto"} {
		r, err := e.Recognize(context.Background(), samples, rate, engine.Options{Language: lang})
		if err != nil {
			t.Fatal(err)
		}
		run := streamClip(t, e, samples, rate, 20*time.Millisecond, 0, engine.Options{Language: lang, InterimResults: true})
		for _, got := range []struct {
			how string
			r   engine.Result
		}{{"offline", r}, {"streamed", run.final}} {
			t.Logf("%s, %s: %v %q", lang, got.how, got.r.Languages, got.r.Text)
			if w := wer(want, got.r.Text); w > 0.1 {
				t.Errorf("%s, %s: WER %.1f%%: %q", lang, got.how, 100*w, got.r.Text)
			}
			if !spokenSpanish(got.r.Languages) {
				t.Errorf("%s, %s: detected %v, want Spanish", lang, got.how, got.r.Languages)
			}
		}
	}
}

// A dictation in automatic detection reports the language it heard, which
// the coordinator hands on with the result (and to history).
func TestLiveNemotronSpanishDictation(t *testing.T) {
	path := testModel(t, nemotronFile)
	samples, rate, want := spanishFixture(t)
	e := New()
	defer func() { _ = e.Unload() }()
	spec := engine.LoadSpec{ID: "nemotron", Path: path}
	if _, err := e.Load(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	d := dictateIn(t, e, spec, "auto", samples, rate, 4)
	t.Logf("%v %q", d.languages, d.text)
	if w := wer(want, d.text); w > 0.1 {
		t.Errorf("WER %.1f%%: %q", 100*w, d.text)
	}
	if !spokenSpanish(d.languages) {
		t.Errorf("result languages %v, want Spanish", d.languages)
	}
}
