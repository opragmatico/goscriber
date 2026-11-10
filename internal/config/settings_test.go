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
