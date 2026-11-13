package config

import (
	"slices"
	"testing"
)

func TestDefaultsPerOS(t *testing.T) {
	tests := []struct {
		goos     string
		shortcut string
		overlay  OverlayStyle
	}{
		{"darwin", "option+space", OverlayLive},
		{"windows", "ctrl+space", OverlayLive},
		{"linux", "ctrl+alt+space", OverlayNone},
		{"freebsd", "alt+space", OverlayLive},
	}
	for _, tt := range tests {
		d := defaultsFor(tt.goos)
		if d.TranscribeShortcut != tt.shortcut || d.OverlayStyle != tt.overlay {
			t.Errorf("%s: shortcut %q overlay %q, want %q %q",
				tt.goos, d.TranscribeShortcut, d.OverlayStyle, tt.shortcut, tt.overlay)
		}
	}
}

func TestDefaultsAreValidAndSelectNoModel(t *testing.T) {
	d := Defaults()
	if fixed := d.normalize(Defaults()); len(fixed) > 0 {
		t.Errorf("defaults fail validation: %v", fixed)
	}
	if d.SelectedModel != "" {
		t.Errorf("SelectedModel = %q; no model may be selected before the user downloads one", d.SelectedModel)
	}
	if d.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", d.SchemaVersion, SchemaVersion)
	}
	if d.ActivationMode != ActivationHoldOrToggle || d.HistoryLimit != 5 || d.ModelUnloadTimeout != Unload5m {
		t.Errorf("defaults drifted from Handy's: %+v", d)
	}
}

func TestCloneIsDeep(t *testing.T) {
	a := Defaults()
	a.CustomWords = []string{"one"}
	a.CustomFillerWords = []string{"um"}
	b := a.Clone()
	b.CustomWords[0] = "changed"
	b.CustomFillerWords[0] = "changed"
	if a.CustomWords[0] != "one" || a.CustomFillerWords[0] != "um" {
		t.Error("Clone shares slices with the original")
	}
}

func TestNormalizeResetsInvalidValues(t *testing.T) {
	def := Defaults()
	s := def.Clone()
	s.ActivationMode = "sometimes"
	s.HoldThresholdMS = 5
	s.AudioFeedbackVolume = 3
	s.OverlayStyle = ""
	s.LogLevel = "trace"
	s.TranscribeShortcut = ""

	fixed := s.normalize(def)
	want := []string{"transcribe_shortcut", "activation_mode", "hold_threshold_ms", "audio_feedback_volume", "overlay_style", "log_level"}
	if !slices.Equal(fixed, want) {
		t.Errorf("fixed = %v, want %v", fixed, want)
	}
	if s.ActivationMode != def.ActivationMode || s.HoldThresholdMS != def.HoldThresholdMS ||
		s.AudioFeedbackVolume != def.AudioFeedbackVolume || s.OverlayStyle != def.OverlayStyle ||
		s.LogLevel != def.LogLevel || s.TranscribeShortcut != def.TranscribeShortcut {
		t.Errorf("invalid values were not reset: %+v", s)
	}
}

func TestNormalizeSpeechDetection(t *testing.T) {
	def := Defaults()
	if def.VADOnset != 0.5 || def.VADOffset != 0.3 || def.MinRecordingMS != 250 || def.SilenceThresholdDB != -60 {
		t.Errorf("speech detection defaults %v %v %v %v", def.VADOnset, def.VADOffset, def.MinRecordingMS, def.SilenceThresholdDB)
	}
	for _, tc := range []struct {
		name       string
		edit       func(*Settings)
		fixed      []string
		onset, off float64
	}{
		{"valid", func(s *Settings) { s.VADOnset, s.VADOffset = 0.7, 0.7 }, nil, 0.7, 0.7},
		{"onset out of range", func(s *Settings) { s.VADOnset = 1 }, []string{"vad_onset"}, 0.5, 0.3},
		{"offset above onset", func(s *Settings) { s.VADOnset, s.VADOffset = 0.6, 0.8 }, []string{"vad_offset"}, 0.6, 0.3},
		{"offset reset stays under a low onset", func(s *Settings) { s.VADOnset, s.VADOffset = 0.2, 0 }, []string{"vad_offset"}, 0.2, 0.2},
		{"thresholds off", func(s *Settings) { s.MinRecordingMS, s.SilenceThresholdDB = 0, -100 }, nil, 0.5, 0.3},
		{
			"thresholds out of range", func(s *Settings) { s.MinRecordingMS, s.SilenceThresholdDB = -1, -10 },
			[]string{"min_recording_ms", "silence_threshold_db"},
			0.5, 0.3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := def.Clone()
			tc.edit(&s)
			if fixed := s.normalize(def); !slices.Equal(fixed, tc.fixed) {
				t.Errorf("fixed = %v, want %v", fixed, tc.fixed)
			}
			if s.VADOnset != tc.onset || s.VADOffset != tc.off {
				t.Errorf("onset %v offset %v, want %v %v", s.VADOnset, s.VADOffset, tc.onset, tc.off)
			}
		})
	}
}
