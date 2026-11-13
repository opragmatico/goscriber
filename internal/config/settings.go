package config

import (
	"encoding/json"
	"errors"
	"runtime"
	"slices"
	"strconv"
	"time"
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

// UnloadTimeout is how many seconds the model stays loaded with nothing to do
// before it is unloaded to free its memory, or UnloadNever. 0 unloads it as
// soon as it is idle. In JSON it is "never" or a whole number of seconds.
type UnloadTimeout int

// Special and limit values of UnloadTimeout.
const (
	UnloadNever      UnloadTimeout = -1    // keep an idle model loaded until the app quits
	MaxUnloadTimeout UnloadTimeout = 86400 // a day
)

// valid reports whether t is UnloadNever or 0 to MaxUnloadTimeout seconds.
func (t UnloadTimeout) valid() bool { return t == UnloadNever || (t >= 0 && t <= MaxUnloadTimeout) }

// Duration returns the timeout as a duration, or a negative one for
// UnloadNever.
func (t UnloadTimeout) Duration() time.Duration {
	if t == UnloadNever {
		return -1
	}
	return time.Duration(t) * time.Second
}

// MarshalJSON writes "never" or the number of seconds.
func (t UnloadTimeout) MarshalJSON() ([]byte, error) {
	if t == UnloadNever {
		return []byte(`"never"`), nil
	}
	return strconv.AppendInt(nil, int64(t), 10), nil
}

var errUnloadTimeout = errors.New(`model_unload_timeout: want "never" or a whole number of seconds`)

// UnmarshalJSON reads "never" or a whole, non-negative number of seconds.
// null leaves t as it is.
func (t *UnloadTimeout) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var word string
	if json.Unmarshal(data, &word) == nil {
		if word != "never" {
			return errUnloadTimeout
		}
		*t = UnloadNever
		return nil
	}
	var secs int
	if json.Unmarshal(data, &secs) != nil || secs < 0 {
		return errUnloadTimeout
	}
	*t = UnloadTimeout(secs)
	return nil
}

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
	ModelUnloadTimeout UnloadTimeout `json:"model_unload_timeout"` // the next dictation reloads an unloaded model

	// Speech detection. With VADEnabled the Silero VAD masks silence in
	// streamed audio before the model sees it: a frame enters speech when
	// its speech probability rises above VADOnset and leaves it when it
	// falls below VADOffset.
	// Recordings shorter than MinRecordingMS, or whose loudest moment is
	// quieter than SilenceThresholdDB (dBFS), never reach the model.
	// SilenceThresholdDB at -100 and MinRecordingMS at 0 turn those checks
	// off.
	VADEnabled         bool    `json:"vad_enabled"`
	VADOnset           float64 `json:"vad_onset"`
	VADOffset          float64 `json:"vad_offset"`
	MinRecordingMS     int     `json:"min_recording_ms"`
	SilenceThresholdDB float64 `json:"silence_threshold_db"`

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
		ModelUnloadTimeout:  30,
		VADEnabled:          true,
		VADOnset:            0.5,
		VADOffset:           0.3,
		MinRecordingMS:      250,
		SilenceThresholdDB:  -60,
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
	check("model_unload_timeout", s.ModelUnloadTimeout.valid(),
		func() { s.ModelUnloadTimeout = def.ModelUnloadTimeout })
	check("vad_onset", s.VADOnset > 0 && s.VADOnset < 1, func() { s.VADOnset = def.VADOnset })
	// The offset must not exceed the onset, or a frame could be in and out of
	// speech at once.
	check("vad_offset", s.VADOffset > 0 && s.VADOffset <= s.VADOnset,
		func() { s.VADOffset = min(def.VADOffset, s.VADOnset) })
	check("min_recording_ms", between(s.MinRecordingMS, 0, 5000), func() { s.MinRecordingMS = def.MinRecordingMS })
	check("silence_threshold_db", s.SilenceThresholdDB >= -100 && s.SilenceThresholdDB <= -20,
		func() { s.SilenceThresholdDB = def.SilenceThresholdDB })
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
