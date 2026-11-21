package config

import (
	"encoding/json"
	"errors"
	"net/url"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
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

// PasteKeys are the keys a clipboard paste presses on Linux. macOS always
// presses Cmd+V.
type PasteKeys string

// Paste keys.
const (
	PasteCtrlV       PasteKeys = "ctrl_v"       // most apps
	PasteShiftInsert PasteKeys = "shift_insert" // apps and terminals alike, in any keyboard layout
	PasteCtrlShiftV  PasteKeys = "ctrl_shift_v" // terminals
)

// TypingTool is what presses keys for Goscriber on Linux: the paste keys,
// direct typing and Return.
type TypingTool string

// Typing tools. Auto uses Goscriber's own X11 key events (XTest) in an X11
// session, and on Wayland the first of wtype, dotool and ydotool that
// works.
const (
	TypingAuto    TypingTool = "auto"
	TypingXdotool TypingTool = "xdotool" // X11
	TypingWtype   TypingTool = "wtype"   // Wayland compositors with the virtual keyboard protocol (not GNOME)
	TypingDotool  TypingTool = "dotool"  // any session, through uinput
	TypingYdotool TypingTool = "ydotool" // any session, through uinput and the ydotoold daemon
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

// WebhookAuth is how the POSTs to the webhook authenticate.
type WebhookAuth string

// Webhook authentication options. The password or token is kept in the OS
// keychain, never in the settings.
const (
	WebhookAuthNone   WebhookAuth = "none"
	WebhookAuthBasic  WebhookAuth = "basic"  // HTTP Basic: WebhookUsername and a password
	WebhookAuthBearer WebhookAuth = "bearer" // Authorization: Bearer and a token
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

// StreamingLatency trades how soon a streaming model shows words against how
// accurate they are: the model looks further ahead in the audio before
// deciding on each word. Offline models ignore it.
type StreamingLatency string

// Streaming latency presets. They map to engine.Latency.
const (
	LatencyLow      StreamingLatency = "low"      // words soonest, at some cost in accuracy
	LatencyBalanced StreamingLatency = "balanced" // the model's own trade-off
	LatencyAccurate StreamingLatency = "accurate" // the most accurate text, shown latest
)

// MaxCustomWordBoost is the strongest custom_word_boost: NeMo-Speech.cpp
// caps a word's boost there for greedy decoding. Values above about 2
// already make Nemotron write custom words where none was said.
const MaxCustomWordBoost = 5

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

	// Shortcuts. The transcribe shortcut's dictations are post-processed
	// with LLMEnabled; the post-processing shortcut's always are (Handy's
	// transcribe_with_post_process). An empty PostProcessShortcut is none.
	TranscribeShortcut  string         `json:"transcribe_shortcut"`
	PostProcessShortcut string         `json:"post_process_shortcut"`
	CancelShortcut      string         `json:"cancel_shortcut"`
	ActivationMode      ActivationMode `json:"activation_mode"`
	HoldThresholdMS     int            `json:"hold_threshold_ms"`

	// Model. SelectedModel is empty until the user downloads a model and
	// picks it: Goscriber never ships or selects one on its own. With
	// PreloadModel the selected model loads at launch, and again in the
	// background when another is selected; without it the first dictation
	// loads it. An unload timeout of 0 turns preloading off, since the
	// model would unload as soon as it was loaded.
	SelectedModel      string        `json:"selected_model"`
	ModelQuant         string        `json:"model_quant"`
	Language           string        `json:"language"`
	Accelerator        Accelerator   `json:"accelerator"`
	ModelUnloadTimeout UnloadTimeout `json:"model_unload_timeout"` // the next dictation reloads an unloaded model
	PreloadModel       bool          `json:"preload_model"`

	// Live transcription. A streaming model (Nemotron) always decodes while
	// the user speaks, looking ahead as far as StreamingLatency says. With
	// LongFormChunking an offline model (Parakeet TDT) decodes the recording
	// in pieces cut at pauses while it goes on, so only the last piece is
	// left when it stops; off, it decodes the whole recording once it stops.
	StreamingLatency StreamingLatency `json:"streaming_latency"`
	LongFormChunking bool             `json:"long_form_chunking"`

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
	// Linux only: the keys a paste presses, and the tool that presses keys.
	PasteKeys  PasteKeys  `json:"paste_keys"`
	TypingTool TypingTool `json:"typing_tool"`

	// Custom vocabulary: names and jargon to spell the user's way. A model
	// with word boosting (Nemotron) is asked to favour CustomWords, by
	// CustomWordBoost, from 0 (off) to MaxCustomWordBoost. Otherwise
	// near-misses of them in the transcript are replaced with their
	// spelling: words whose letters differ by less than
	// WordCorrectionThreshold, a share from 0 (off) to 1.
	CustomWords             []string `json:"custom_words"`
	CustomWordBoost         float64  `json:"custom_word_boost"`
	WordCorrectionThreshold float64  `json:"word_correction_threshold"`

	// Filler words ("um", "uh") are removed from transcripts with
	// FillerWordRemoval. A nil CustomFillerWords means the built-in lists,
	// chosen by the transcript's language; a list replaces them, and an
	// empty one means the user removed every filler word.
	FillerWordRemoval bool     `json:"filler_word_removal"`
	CustomFillerWords []string `json:"custom_filler_words"`

	// LLM post-processing (docs/llm-post-processing.md). With LLMEnabled a
	// language model cleans each transcript after the text cleanup: the
	// model LLMModel of LLMProvider, asked with LLMPrompt, where
	// "${output}" stands for the transcript. LLMBaseURL is where the
	// provider is reached: set from the provider when it is picked, and
	// typed in for Ollama elsewhere than its default address and for
	// LLMCustom. An empty LLMModel is the provider's default, if it has one
	// (the model suggested for the computer's memory, for a provider on
	// this computer), and an empty LLMPrompt the built-in one. API keys are
	// not kept here but in the OS keychain, by base URL.
	LLMEnabled  bool        `json:"llm_enabled"`
	LLMProvider LLMProvider `json:"llm_provider"`
	LLMBaseURL  string      `json:"llm_base_url"`
	LLMModel    string      `json:"llm_model"`
	LLMPrompt   string      `json:"llm_prompt"`

	// History
	HistoryLimit       int                `json:"history_limit"`
	RecordingRetention RecordingRetention `json:"recording_retention"`

	// WebhookURL receives every finished transcription with an HTTP POST
	// (internal/webhook); "" sends nothing. CheckWebhookURL says which URLs
	// it takes. WebhookAuth is how the POSTs authenticate: the password or
	// token is in the OS keychain under the URL, and WebhookUsername is
	// Basic's user name.
	WebhookURL      string      `json:"webhook_url"`
	WebhookAuth     WebhookAuth `json:"webhook_auth"`
	WebhookUsername string      `json:"webhook_username"`

	// App. DebugMode reveals debug-only UI such as the Debug settings page;
	// --debug turns it on for one session without saving it. Autostart is
	// the OS's login item, which opens Goscriber hidden when the user logs
	// in; the app keeps the two in step.
	LogLevel     LogLevel `json:"log_level"`
	DebugMode    bool     `json:"debug_mode"`
	StartHidden  bool     `json:"start_hidden"`
	ShowTrayIcon bool     `json:"show_tray_icon"`
	Autostart    bool     `json:"autostart"`

	// EngineInProcess runs the speech engine inside the app instead of in a
	// worker process, for profiling: a crash in the engine then takes the
	// app down. A debug toggle; it applies at the next launch, and
	// --engine-in-process turns it on for one session.
	EngineInProcess bool `json:"engine_in_process"`
}

// Defaults returns the settings for a fresh install on this OS.
func Defaults() Settings { return defaultsFor(runtime.GOOS) }

// defaultsFor follows Handy's defaults where they apply.
func defaultsFor(goos string) Settings {
	return Settings{
		SchemaVersion:       SchemaVersion,
		TranscribeShortcut:  defaultShortcut(goos),
		PostProcessShortcut: defaultPostProcessShortcut(goos),
		CancelShortcut:      "escape",
		ActivationMode:      ActivationHoldOrToggle,
		HoldThresholdMS:     300,
		ModelQuant:          "Q8_0",
		Language:            "auto",
		Accelerator:         AcceleratorAuto,
		ModelUnloadTimeout:  30,
		PreloadModel:        true,
		StreamingLatency:    LatencyBalanced,
		LongFormChunking:    true,
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
		PasteDelayAfterMS:   defaultPasteDelayAfter(goos),
		PasteKeys:           defaultPasteKeys(goos),
		TypingTool:          TypingAuto,
		HistoryLimit:        5,
		RecordingRetention:  RetentionPreserveLimit,
		WebhookAuth:         WebhookAuthNone,
		LogLevel:            LogInfo,
		ShowTrayIcon:        true,

		// Custom vocabulary, measured on Nemotron (docs/architecture.md).
		CustomWordBoost:         1,
		WordCorrectionThreshold: 0.18, // Handy's

		FillerWordRemoval: true,

		LLMProvider: LLMOllama,
		LLMBaseURL:  DefaultLLMBaseURL,
	}
}

// DefaultLLMBaseURL is Ollama's OpenAI-compatible API on this computer.
const DefaultLLMBaseURL = "http://localhost:11434/v1"

// LLMProvider is the service the llm_provider setting sends transcripts
// to for post-processing.
type LLMProvider string

// The providers the settings offer. LLMCustom is any other server with an
// OpenAI-compatible API, such as LM Studio or llama.cpp's llama-server.
const (
	LLMGemini     LLMProvider = "gemini"
	LLMOpenAI     LLMProvider = "openai"
	LLMClaude     LLMProvider = "claude"
	LLMOpenRouter LLMProvider = "openrouter"
	LLMGroq       LLMProvider = "groq"
	LLMOllama     LLMProvider = "ollama"
	LLMCustom     LLMProvider = "custom"
)

// LLMProviders are the providers in the order the settings list them.
var LLMProviders = []LLMProvider{LLMGemini, LLMOpenAI, LLMClaude, LLMOpenRouter, LLMGroq, LLMOllama, LLMCustom}

// BaseURL is where the provider is reached unless the user typed another
// address: Ollama on this computer, and "" for LLMCustom, which has none.
func (p LLMProvider) BaseURL() string {
	switch p {
	case LLMGemini:
		return "https://generativelanguage.googleapis.com/v1beta/openai"
	case LLMOpenAI:
		return "https://api.openai.com/v1"
	case LLMClaude:
		return "https://api.anthropic.com"
	case LLMOpenRouter:
		return "https://openrouter.ai/api/v1"
	case LLMGroq:
		return "https://api.groq.com/openai/v1"
	case LLMOllama:
		return DefaultLLMBaseURL
	}
	return ""
}

// LLMProviderFor is the provider reached at baseURL: the one whose address
// it is, trailing slash aside, else LLMCustom. It reads the provider of a
// settings file written before llm_provider existed.
func LLMProviderFor(baseURL string) LLMProvider {
	u := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	for _, p := range LLMProviders {
		if b := p.BaseURL(); b != "" && strings.EqualFold(b, u) {
			return p
		}
	}
	return LLMCustom
}

// validBaseURL reports whether s is an http or https URL with a host.
func validBaseURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
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

// defaultPostProcessShortcut is Handy's transcribe_with_post_process
// default: the transcribe shortcut with Shift added, except on Windows,
// where Handy's is Ctrl+Shift+Space.
func defaultPostProcessShortcut(goos string) string {
	switch goos {
	case "darwin":
		return "option+shift+space"
	case "windows":
		return "ctrl+shift+space"
	case "linux":
		return "ctrl+alt+shift+space"
	default:
		return "alt+shift+space"
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

// Shift+Insert pastes in terminals as well as in other apps, and does not
// depend on the keyboard layout, so Linux defaults to it, as Handy does.
func defaultPasteKeys(goos string) PasteKeys {
	if goos == "linux" {
		return PasteShiftInsert
	}
	return PasteCtrlV
}

// On X11 the app reads the clipboard from Goscriber when it handles the
// paste keys, which may come well after they are pressed; Handy waits 500
// ms on Linux before putting the clipboard back.
func defaultPasteDelayAfter(goos string) int {
	if goos == "linux" {
		return 500
	}
	return 60
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
	check("streaming_latency", oneOf(s.StreamingLatency, LatencyLow, LatencyBalanced, LatencyAccurate),
		func() { s.StreamingLatency = def.StreamingLatency })
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
	check("paste_keys", oneOf(s.PasteKeys, PasteCtrlV, PasteShiftInsert, PasteCtrlShiftV),
		func() { s.PasteKeys = def.PasteKeys })
	check("typing_tool", oneOf(s.TypingTool, TypingAuto, TypingXdotool, TypingWtype, TypingDotool, TypingYdotool),
		func() { s.TypingTool = def.TypingTool })
	check("paste_delay_ms", between(s.PasteDelayMS, 0, 5000), func() { s.PasteDelayMS = def.PasteDelayMS })
	check("paste_delay_after_ms", between(s.PasteDelayAfterMS, 0, 5000),
		func() { s.PasteDelayAfterMS = def.PasteDelayAfterMS })
	check("custom_word_boost", s.CustomWordBoost >= 0 && s.CustomWordBoost <= MaxCustomWordBoost,
		func() { s.CustomWordBoost = def.CustomWordBoost })
	check("word_correction_threshold", s.WordCorrectionThreshold >= 0 && s.WordCorrectionThreshold <= 1,
		func() { s.WordCorrectionThreshold = def.WordCorrectionThreshold })
	check("history_limit", between(s.HistoryLimit, 0, 100000), func() { s.HistoryLimit = def.HistoryLimit })
	check("recording_retention",
		oneOf(s.RecordingRetention, RetentionNever, RetentionPreserveLimit, RetentionDays3, RetentionWeeks2, RetentionMonths3),
		func() { s.RecordingRetention = def.RecordingRetention })
	check("llm_base_url", validBaseURL(s.LLMBaseURL), func() { s.LLMBaseURL = def.LLMBaseURL })
	check("llm_provider", oneOf(s.LLMProvider, LLMProviders...),
		func() { s.LLMProvider = LLMProviderFor(s.LLMBaseURL) })
	check("webhook_url", CheckWebhookURL(s.WebhookURL) == nil, func() { s.WebhookURL = def.WebhookURL })
	check("webhook_auth", oneOf(s.WebhookAuth, WebhookAuthNone, WebhookAuthBasic, WebhookAuthBearer),
		func() { s.WebhookAuth = def.WebhookAuth })
	check("webhook_username", CheckWebhookUsername(s.WebhookUsername) == nil, func() { s.WebhookUsername = def.WebhookUsername })
	check("log_level", oneOf(s.LogLevel, LogDebug, LogInfo, LogWarn, LogError), func() { s.LogLevel = def.LogLevel })
	return fixed
}

// CheckWebhookURL reports whether u can be the webhook_url setting: empty,
// for no webhook, or an absolute http or https URL with a host. A user name
// and password in the URL are refused: they belong with Basic
// authentication, whose password the keychain keeps.
func CheckWebhookURL(u string) error {
	if u == "" {
		return nil
	}
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" || p.Hostname() == "" {
		return errors.New("enter a URL that starts with https:// or http://")
	}
	if p.User != nil {
		return errors.New("leave the user name and password out of the URL: choose Basic authentication instead")
	}
	return nil
}

// CheckWebhookUsername reports whether name can be Basic's user name:
// HTTP Basic cannot carry a colon in it (RFC 7617), nor control
// characters.
func CheckWebhookUsername(name string) error {
	if strings.Contains(name, ":") {
		return errors.New("a user name cannot hold a colon")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errors.New("a user name cannot hold control characters")
	}
	return nil
}

func oneOf[T comparable](v T, allowed ...T) bool { return slices.Contains(allowed, v) }

func between(v, lo, hi int) bool { return v >= lo && v <= hi }
