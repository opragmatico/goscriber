package pipeline

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/fake"
)

func TestCleanupStageCorrectsCustomWords(t *testing.T) {
	settings := config.Defaults()
	settings.CustomWords = []string{"Queequeg"}
	const in = "I found Quequeg's arm."
	for _, tt := range []struct {
		name      string
		boosted   bool
		threshold float64
		want      string
	}{
		{"not boosted", false, 0.18, "I found Queequeg's arm."},
		{"boosted by the model", true, 0.18, in},
		{"correction off", false, 0, in},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := settings.Clone()
			s.WordCorrectionThreshold = tt.threshold
			got, err := CleanupStage{}.Process(context.Background(), in, SessionInfo{Boosted: tt.boosted, Settings: s})
			if err != nil || got != tt.want {
				t.Errorf("Process = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestCleanupStageRemovesFillers(t *testing.T) {
	for _, tt := range []struct {
		name     string
		in       string
		language string
		edit     func(*config.Settings)
		want     string
	}{
		{"English", "Um, so I was, uh, thinking.", "en-US", nil, "So I was, thinking."},
		{"unknown language keeps um", "Um, so I was, uh, thinking.", "", nil, "Um, so I was, thinking."},
		{"Portuguese", "Eu tenho um carro, uh.", "pt-BR", nil, "Eu tenho um carro."},
		{"off", "Um, so  I was, uh, thinking.", "en-US", func(s *config.Settings) { s.FillerWordRemoval = false }, "Um, so I was, uh, thinking."},
		{"the user's list", "Um, it basically works.", "en-US", func(s *config.Settings) { s.CustomFillerWords = []string{"basically"} }, "Um, it works."},
		{"the user removed every filler", "Um, it works.", "en-US", func(s *config.Settings) { s.CustomFillerWords = []string{} }, "Um, it works."},
		{"only fillers", "Uh, um.", "en-US", nil, ""},
		{"a lone full stop", ".", "en-US", nil, ""},
		{"corrected, then fillers removed", "Um, ask Kowalczik.", "en-US", func(s *config.Settings) { s.CustomWords = []string{"Kowalczyk"} }, "Ask Kowalczyk."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := config.Defaults()
			if tt.edit != nil {
				tt.edit(&s)
			}
			got, err := CleanupStage{}.Process(context.Background(), tt.in, SessionInfo{TextLanguage: tt.language, Settings: s})
			if err != nil || got != tt.want {
				t.Errorf("Process(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
		})
	}
}

func TestTextLanguage(t *testing.T) {
	for _, tt := range []struct {
		name     string
		m        Model
		detected []string
		want     string
	}{
		{"detected", Model{Language: "es-ES"}, []string{"es-US"}, "es-US"},
		{"asked for", Model{Language: "es-ES"}, nil, "es-ES"},
		{"automatic, nothing detected", Model{Language: "auto"}, nil, ""},
		{"the model's only language", Model{FixedLanguage: "en"}, nil, "en"},
		{"unknown", Model{}, nil, ""},
	} {
		if got := textLanguage(tt.m, tt.detected); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}

// A model with word boosting is asked to favour the custom words, live and
// whole; the text stages learn that it was. One without them gets no
// phrases, and the cleanup stage corrects its transcript instead.
func TestCustomWordsBoostOrCorrect(t *testing.T) {
	for _, tt := range []struct {
		name     string
		boosting bool
		boost    float64
		script   fake.Script
		phrases  []string
	}{
		{"streaming model that boosts", true, 1, streaming(fake.Dictation("I found Quequeg's arm", 0)), []string{"Queequeg", "NVIDIA"}},
		{"boost off", true, 0, streaming(fake.Dictation("I found Quequeg's arm", 0)), nil},
		{"offline model without boosting", false, 1, fake.Script{Replies: []fake.Reply{fake.Transcript("I found Quequeg's arm")}}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var infos []SessionInfo
			r := newRig(t, rigOptions{
				script: tt.script,
				settings: func(s *config.Settings) {
					s.CustomWords = []string{" Queequeg", "", "NVIDIA", "queequeg"}
					s.CustomWordBoost = tt.boost
				},
				stages: Stages{Cleanup: TextStageFunc(func(ctx context.Context, text string, info SessionInfo) (string, error) {
					mu.Lock()
					infos = append(infos, info)
					mu.Unlock()
					return CleanupStage{}.Process(ctx, text, info)
				})},
			})
			m := testModel
			m.WordBoosting = tt.boosting
			r.models.mu.Lock()
			r.models.model = m
			r.models.mu.Unlock()

			r.c.Start()
			r.events.waitState(t, StateRecording, 1)
			r.c.Stop()
			r.events.waitState(t, StateIdle, 1)

			var opts []engine.Options
			for _, s := range r.eng.Streams() {
				opts = append(opts, s.Options())
			}
			for _, rec := range r.eng.Recognitions() {
				opts = append(opts, rec.Options)
			}
			if len(opts) == 0 {
				t.Fatal("nothing was decoded")
			}
			for _, o := range opts {
				if !slices.Equal(o.Phrases, tt.phrases) || (tt.phrases != nil && o.Boost != float32(tt.boost)) {
					t.Errorf("options %+v, want phrases %q boosted by %v", o, tt.phrases, tt.boost)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(infos) != 1 || infos[0].Boosted != (tt.phrases != nil) || infos[0].Settings.CustomWordBoost != tt.boost {
				t.Errorf("stage infos %+v", infos)
			}
			want := "I found Queequeg's arm"
			if tt.phrases != nil {
				want = "I found Quequeg's arm" // the fake model does not boost
			}
			if got := r.out.delivered(); !slices.Equal(got, []string{want}) {
				t.Errorf("delivered %q, want %q", got, want)
			}
		})
	}
}
