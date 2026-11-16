//go:build cgo && nemo

package nemo

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/pipeline"
)

// A ten-minute WAV file, as the History page transcribes one: read back
// from disk, decoded by Coordinator.Transcribe with progress, through a
// stream for Nemotron and in chunks for Parakeet, every word kept.
func TestTranscribeTenMinuteFile(t *testing.T) {
	for _, tt := range []struct{ name, file string }{
		{"parakeet", "parakeet-tdt-0.6b-v2.q8_0.gguf"},
		{"nemotron", nemotronFile},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := testModel(t, tt.file)
			clip, rate, ref := fixture(t)
			gap := make([]float32, rate*4/10)
			var samples []float32
			var want []string
			for len(samples) < 10*60*rate {
				samples = append(samples, clip...)
				samples = append(samples, gap...)
				want = append(want, ref)
			}
			wav := filepath.Join(t.TempDir(), "ten-minutes.wav")
			if err := audio.WriteWAVFile(wav, samples, rate); err != nil {
				t.Fatal(err)
			}
			read, readRate, err := audio.ReadFile(wav)
			if err != nil {
				t.Fatal(err)
			}

			e := New()
			defer func() { _ = e.Unload() }()
			c := pipeline.New(pipeline.Options{
				Recorder: &playback{rate: rate, levels: make(chan audio.Level)},
				Engine:   e,
				Models:   oneModel{pipeline.Model{Spec: engine.LoadSpec{ID: tt.name, Path: path}, Name: tt.name, Language: "en-US"}},
				Output:   &timedOutput{},
				Notices:  noNotices{t},
				Settings: config.Defaults,
				Stages:   pipeline.Stages{Cleanup: pipeline.CleanupStage{}},
				Log:      slog.New(slog.DiscardHandler),
			})
			defer c.Close()

			var reports []time.Duration
			start := time.Now()
			got, err := c.Transcribe(context.Background(), read, readRate, pipeline.TranscribeOptions{
				Progress: func(done, _ time.Duration) { reports = append(reports, done) },
			})
			if err != nil {
				t.Fatal(err)
			}
			took := time.Since(start)
			length := time.Duration(len(read)) * time.Second / time.Duration(readRate)
			words, wantWords := normalizeWords(got.RawText), normalizeWords(strings.Join(want, " "))
			t.Logf("%v of audio (%d takes) in %v, %d progress reports; WER %.2f%%",
				length.Round(time.Second), len(want), took.Round(time.Millisecond), len(reports),
				100*wer(strings.Join(want, " "), got.RawText))
			if len(reports) < 60 || reports[len(reports)-1] != length {
				t.Errorf("progress %v…, want a report every 10 s up to %v", reports[:min(3, len(reports))], length)
			}
			if !slices.Equal(words, wantWords) {
				t.Errorf("transcript has %d words, want the %d spoken, none lost or repeated", len(words), len(wantWords))
			}
		})
	}
}
