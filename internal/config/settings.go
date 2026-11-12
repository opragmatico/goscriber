package config

import (
	"runtime"
	"slices"
)

// ActivationMode is how the transcribe shortcut's key events drive a recording.
type ActivationMode string

// Activation modes.
const (
	ActivationToggle       ActivationMode = "toggle"         // press to start, press again to stop
	ActivationPushToTalk   ActivationMode = "push_to_talk"   // hold to record, release to stop
	ActivationHoldOrToggle ActivationMode = "hold_or_toggle" // hold for push-to-talk, tap to toggle
)

// OverlayStyle selects the recording overlay.
type OverlayStyle string

// Overlay styles.
const (
	OverlayNone    OverlayStyle = "none"
	OverlayMinimal OverlayStyle = "minimal"
	OverlayLive    OverlayStyle = "live" // grows to show live transcription text
)

// OverlayPosition places the overlay on the screen.
type OverlayPosition string

// Overlay positions.
const (
	OverlayTop    OverlayPosition = "top"
	OverlayBottom OverlayPosition = "bottom"
)

// PasteMethod is how a transcript reaches the focused app.
type PasteMethod string

// Paste methods.
const (
	PasteClipboard PasteMethod = "paste"  // clipboard + Cmd/Ctrl+V
	PasteDirect    PasteMethod = "direct" // type the characters
	PasteNone      PasteMethod = "none"   // copy only, never paste
)

// ClipboardHandling says what happens to the clipboard after a paste.
type ClipboardHandling string

// Clipboard handling options.
const (
	ClipboardRestore ClipboardHandling = "restore" // put the previous clipboard back
	ClipboardKeep    ClipboardHandling = "keep"    // leave the transcript on the clipboard
)

// RecordingRetention says how long saved recordings are kept.
type RecordingRetention string

// Recording retention options.
const (
	RetentionNever         RecordingRetention = "never"          // do not keep recordings
	RetentionPreserveLimit RecordingRetention = "preserve_limit" // keep those of the last HistoryLimit entries
	RetentionDays3         RecordingRetention = "days_3"
	RetentionWeeks2        RecordingRetention = "weeks_2"
	RetentionMonths3       RecordingRetention = "months_3"
)

// UnloadTimeout is how long an idle model stays loaded.
type UnloadTimeout string

// Model unload timeouts.
const (
	UnloadNever       UnloadTimeout = "never"
	UnloadImmediately UnloadTimeout = "immediately"
	Unload2m          UnloadTimeout = "2m"
	Unload5m          UnloadTimeout = "5m"
	Unload10m         UnloadTimeout = "10m"
	Unload15m         UnloadTimeout = "15m"
	Unload1h          UnloadTimeout = "1h"
)

// Accelerator selects where inference runs.
type Accelerator string

// Accelerators.
const (
	AcceleratorAuto Accelerator = "auto" // Metal / GPU when available, else CPU
	AcceleratorCPU  Accelerator = "cpu"
)

// LogLevel is the minimum level written to the logs.
type LogLevel string

// Log levels.
const (
	LogDebug LogLevel = "debug"
	LogInfo  LogLevel = "info"
	LogWarn  LogLevel = "warn"
	LogError LogLevel = "error"
)

// Settings is everything the user can configure. It is persisted as JSON;
// fields missing from the file take their value from Defaults, so adding a
// field with a sensible default needs no migration.
type Settings struct {
	SchemaVersion int `json:"schema_version"`

	// Shortcuts
	TranscribeShortcut string         `json:"transcribe_shortcut"`
	CancelShortcut     string         `json:"cancel_shortcut"`
	ActivationMode     ActivationMode `json:"activation_mode"`
	HoldThresholdMS    int            `json:"hold_threshold_ms"`

	// Model. SelectedModel is empty until the user downloads a model and
	// picks it: Goscriber never ships or selects one on its own.
	SelectedModel      string        `json:"selected_model"`
	ModelQuant         string        `json:"model_quant"`
	Language           string        `json:"language"`
	Accelerator        Accelerator   `json:"accelerator"`
	ModelUnloadTimeout UnloadTimeout `json:"model_unload_timeout"`
	VADEnabled         bool          `json:"vad_enabled"`

	// Audio. Empty device names mean the system default. An always-on
	// microphone stays open between recordings and keeps ~200 ms of audio
	// from before the shortcut, so the first syllable is not lost.
	Microphone          string  `json:"microphone"`
	AlwaysOnMicrophone  bool    `json:"always_on_microphone"`
	OutputDevice        string  `json:"output_device"`
	AudioFeedback       bool    `json:"audio_feedback"`
	AudioFeedbackVolume float64 `json:"audio_feedback_volume"`
	MuteWhileRecording  bool    `json:"mute_while_recording"`

	// Overlay
	OverlayStyle    OverlayStyle    `json:"overlay_style"`
	OverlayPosition OverlayPosition `json:"overlay_position"`

	// Output
	PasteMethod         PasteMethod       `json:"paste_method"`
	ClipboardHandling   ClipboardHandling `json:"clipboard_handling"`
	PasteDelayMS        int               `json:"paste_delay_ms"`
	PasteDelayAfterMS   int               `json:"paste_delay_after_ms"`
	AutoSubmit          bool              `json:"auto_submit"`
	AppendTrailingSpace bool              `json:"append_trailing_space"`

	// Text. A nil CustomFillerWords means the built-in list; an empty one
	// means the user removed every filler word.
	CustomWords       []string `json:"custom_words"`
	FillerWordRemoval bool     `json:"filler_word_removal"`
	CustomFillerWords []string `json:"custom_filler_words"`

	// History
	HistoryLimit       int                `json:"history_limit"`
	RecordingRetention RecordingRetention `json:"recording_retention"`

	// App. DebugMode reveals debug-only UI such as the Debug settings page;
	// --debug turns it on for one session without saving it.
	LogLevel     LogLevel `json:"log_level"`
	DebugMode    bool     `json:"debug_mode"`
	StartHidden  bool     `json:"start_hidden"`
	ShowTrayIcon bool     `json:"show_tray_icon"`
	Autostart    bool     `json:"autostart"`
}

// Defaults returns the settings for a fresh install on this OS.
func Defaults() Settings { return defaultsFor(runtime.GOOS) }

// defaultsFor follows Handy's defaults where they apply.
func defaultsFor(goos string) Settings {
	return Settings{
		SchemaVersion:       SchemaVersion,
		TranscribeShortcut:  defaultShortcut(goos),
		CancelShortcut:      "escape",
		ActivationMode:      ActivationHoldOrToggle,
		HoldThresholdMS:     300,
		ModelQuant:          "Q8_0",
		Language:            "auto",
		Accelerator:         AcceleratorAuto,
		ModelUnloadTimeout:  Unload5m,
		VADEnabled:          true,
		AudioFeedbackVolume: 1.0,
		OverlayStyle:        defaultOverlayStyle(goos),
		OverlayPosition:     OverlayBottom,
		PasteMethod:         PasteClipboard,
		ClipboardHandling:   ClipboardRestore,
		PasteDelayMS:        60,
		PasteDelayAfterMS:   60,
		HistoryLimit:        5,
		RecordingRetention:  RetentionPreserveLimit,
		LogLevel:            LogInfo,
		ShowTrayIcon:        true,
	}
}

func defaultShortcut(goos string) string {
	switch goos {
	case "darwin":
		return "option+space"
	case "windows":
		return "ctrl+space"
	case "linux":
		return "ctrl+alt+space"
	default:
		return "alt+space"
	}
}

// The overlay steals focus on several Linux desktops, so it starts hidden
// there, as in Handy.
func defaultOverlayStyle(goos string) OverlayStyle {
	if goos == "linux" {
		return OverlayNone
	}
	return OverlayLive
}

// Clone returns a deep copy, so callers can change it without affecting the
// original.
func (s Settings) Clone() Settings {
	s.CustomWords = slices.Clone(s.CustomWords)
	s.CustomFillerWords = slices.Clone(s.CustomFillerWords)
	return s
}

// normalize replaces invalid values with their default and returns the JSON
// names of the fields it changed.
func (s *Settings) normalize(def Settings) []string {
	var fixed []string
	check := func(name string, ok bool, reset func()) {
		if !ok {
			reset()
			fixed = append(fixed, name)
		}
	}

	check("transcribe_shortcut", s.TranscribeShortcut != "", func() { s.TranscribeShortcut = def.TranscribeShortcut })
	check("cancel_shortcut", s.CancelShortcut != "", func() { s.CancelShortcut = def.CancelShortcut })
	check("activation_mode", oneOf(s.ActivationMode, ActivationToggle, ActivationPushToTalk, ActivationHoldOrToggle),
		func() { s.ActivationMode = def.ActivationMode })
	check("hold_threshold_ms", between(s.HoldThresholdMS, 50, 2000), func() { s.HoldThresholdMS = def.HoldThresholdMS })
	check("model_quant", s.ModelQuant != "", func() { s.ModelQuant = def.ModelQuant })
	check("language", s.Language != "", func() { s.Language = def.Language })
	check("accelerator", oneOf(s.Accelerator, AcceleratorAuto, AcceleratorCPU), func() { s.Accelerator = def.Accelerator })
	check("model_unload_timeout",
		oneOf(s.ModelUnloadTimeout, UnloadNever, UnloadImmediately, Unload2m, Unload5m, Unload10m, Unload15m, Unload1h),
		func() { s.ModelUnloadTimeout = def.ModelUnloadTimeout })
	check("audio_feedback_volume", s.AudioFeedbackVolume >= 0 && s.AudioFeedbackVolume <= 1,
		func() { s.AudioFeedbackVolume = def.AudioFeedbackVolume })
	check("overlay_style", oneOf(s.OverlayStyle, OverlayNone, OverlayMinimal, OverlayLive),
		func() { s.OverlayStyle = def.OverlayStyle })
	check("overlay_position", oneOf(s.OverlayPosition, OverlayTop, OverlayBottom),
		func() { s.OverlayPosition = def.OverlayPosition })
	check("paste_method", oneOf(s.PasteMethod, PasteClipboard, PasteDirect, PasteNone),
		func() { s.PasteMethod = def.PasteMethod })
	check("clipboard_handling", oneOf(s.ClipboardHandling, ClipboardRestore, ClipboardKeep),
		func() { s.ClipboardHandling = def.ClipboardHandling })
	check("paste_delay_ms", between(s.PasteDelayMS, 0, 5000), func() { s.PasteDelayMS = def.PasteDelayMS })
	check("paste_delay_after_ms", between(s.PasteDelayAfterMS, 0, 5000),
		func() { s.PasteDelayAfterMS = def.PasteDelayAfterMS })
	check("history_limit", between(s.HistoryLimit, 0, 100000), func() { s.HistoryLimit = def.HistoryLimit })
	check("recording_retention",
		oneOf(s.RecordingRetention, RetentionNever, RetentionPreserveLimit, RetentionDays3, RetentionWeeks2, RetentionMonths3),
		func() { s.RecordingRetention = def.RecordingRetention })
	check("log_level", oneOf(s.LogLevel, LogDebug, LogInfo, LogWarn, LogError), func() { s.LogLevel = def.LogLevel })
	return fixed
}

func oneOf[T comparable](v T, allowed ...T) bool { return slices.Contains(allowed, v) }

func between(v, lo, hi int) bool { return v >= lo && v <= hi }
