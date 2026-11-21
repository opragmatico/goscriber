package ui

import (
	"fmt"
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/config"
)

// unloadChoices are the idle times the unload picker offers; another value
// from the settings file is shown as it is.
var unloadChoices = []choice[config.UnloadTimeout]{
	{config.UnloadNever, "Never"},
	{0, "As soon as it is idle"},
	{15, "After 15 seconds"},
	{30, "After 30 seconds"},
	{60, "After 1 minute"},
	{120, "After 2 minutes"},
	{300, "After 5 minutes"},
	{600, "After 10 minutes"},
	{1800, "After 30 minutes"},
	{3600, "After 1 hour"},
}

func formatUnload(t config.UnloadTimeout) string { return fmt.Sprintf("After %d seconds", int(t)) }

// formatOff shows 0 as "off", other values to two decimals.
func formatOff(v float64) string {
	if v == 0 {
		return "off"
	}
	return formatFraction(v)
}

func formatDB(v float64) string {
	if v <= -100 {
		return "off"
	}
	return fmt.Sprintf("%.0f dBFS", v)
}

func (w *SettingsWindow) advancedPage() fyne.CanvasObject {
	onset := sliderSetting(w, "vad_onset", func(s *config.Settings) *float64 { return &s.VADOnset }, 0.05, 0.95, 0.05, formatFraction)
	offset := sliderSetting(w, "vad_offset", func(s *config.Settings) *float64 { return &s.VADOffset }, 0.05, 0.95, 0.05, formatFraction)
	// The offset may not exceed the onset: moving either keeps them in order.
	keepOrder := func(s *config.Settings) { s.VADOffset = min(s.VADOffset, s.VADOnset) }
	onset.fix, offset.fix = keepOrder, keepOrder
	accelerator, acceleratorHint := w.acceleratorSetting()
	autostart, autostartHint := w.autostartSetting()

	output := []fyne.CanvasObject{
		w.row("Paste method", "How the transcript reaches the app you are using.",
			choiceSetting(w, "paste_method", func(s *config.Settings) *config.PasteMethod { return &s.PasteMethod },
				[]choice[config.PasteMethod]{
					{config.PasteClipboard, "Paste through the clipboard"},
					{config.PasteDirect, "Type the text"},
					{config.PasteNone, "Copy only, never paste"},
				}, nil)),
	}
	output = append(output, w.linuxOutputRows()...)
	output = append(output,
		w.row("Clipboard", "After pasting through the clipboard.",
			choiceSetting(w, "clipboard_handling", func(s *config.Settings) *config.ClipboardHandling { return &s.ClipboardHandling },
				[]choice[config.ClipboardHandling]{
					{config.ClipboardRestore, "Put back what was there"},
					{config.ClipboardKeep, "Keep the transcript"},
				}, nil)),
		w.row("Delay before pasting", "Between writing the clipboard and pressing the paste keys.",
			sliderSetting(w, "paste_delay_ms", func(s *config.Settings) *int { return &s.PasteDelayMS }, 0, 1000, 10, formatMS)),
		w.row("Delay after pasting", "Before the clipboard is put back, so the app has read it.",
			sliderSetting(w, "paste_delay_after_ms", func(s *config.Settings) *int { return &s.PasteDelayAfterMS }, 0, 1000, 10, formatMS)),
		w.row("Add a trailing space", "So the next dictation does not run into this one.",
			boolSetting(w, "append_trailing_space", func(s *config.Settings) *bool { return &s.AppendTrailingSpace })),
		w.row("Press Return after pasting", "Sends the message in chat apps.",
			boolSetting(w, "auto_submit", func(s *config.Settings) *bool { return &s.AutoSubmit })),
	)

	return settingsPage("Advanced",
		group("App",
			w.row("Start hidden", "Open no window at launch. With the tray icon off, the settings window opens anyway.",
				boolSetting(w, "start_hidden", func(s *config.Settings) *bool { return &s.StartHidden })),
			w.row("Show the tray icon", "Takes effect at the next launch. Without it, this window opens at launch and closing it quits.",
				boolSetting(w, "show_tray_icon", func(s *config.Settings) *bool { return &s.ShowTrayIcon })),
			w.row("Launch at login", autostartHint, autostart),
			w.row("Unload the model", "Frees its memory once dictation has been idle this long. The next dictation loads it again.",
				choiceSetting(w, "model_unload_timeout", func(s *config.Settings) *config.UnloadTimeout { return &s.ModelUnloadTimeout },
					unloadChoices, formatUnload)),
			w.row("Load the model at launch", "So the first dictation does not wait for it; a model you switch to loads in the background too. "+
				"Off, the first dictation loads it. Nothing loads early while the model unloads as soon as it is idle.",
				boolSetting(w, "preload_model", func(s *config.Settings) *bool { return &s.PreloadModel })),
		),
		group("Output", output...),
		group("Transcription",
			w.row("Run the model on", acceleratorHint, accelerator),
			w.row("Live text", "For streaming models: how soon words show against how accurate they are.",
				choiceSetting(w, "streaming_latency", func(s *config.Settings) *config.StreamingLatency { return &s.StreamingLatency },
					[]choice[config.StreamingLatency]{
						{config.LatencyLow, "Fastest"},
						{config.LatencyBalanced, "Balanced"},
						{config.LatencyAccurate, "Most accurate"},
					}, nil)),
			w.row("Decode while recording", "Parakeet decodes the recording in pieces at your pauses, so long dictations are ready sooner.",
				boolSetting(w, "long_form_chunking", func(s *config.Settings) *bool { return &s.LongFormChunking })),
			w.row("Keep the microphone open", "Keeps the start of what you say from before the shortcut. The microphone indicator stays on.",
				boolSetting(w, "always_on_microphone", func(s *config.Settings) *bool { return &s.AlwaysOnMicrophone })),
			w.row("Voice activity detection", "Silero VAD hides silence from streaming models.",
				boolSetting(w, "vad_enabled", func(s *config.Settings) *bool { return &s.VADEnabled })),
			w.row("Speech starts above", "The VAD's speech probability to enter speech.", onset),
			w.row("Speech ends below", "The probability to leave speech; at most the start.", offset),
			w.row("Shortest recording", "Shorter recordings are ignored. 0 turns this off.",
				sliderSetting(w, "min_recording_ms", func(s *config.Settings) *int { return &s.MinRecordingMS }, 0, 2000, 50, formatMS)),
			w.row("Silence threshold", "Recordings never louder than this are ignored as silence.",
				sliderSetting(w, "silence_threshold_db", func(s *config.Settings) *float64 { return &s.SilenceThresholdDB }, -100, -20, 1, formatDB)),
		),
		group("Text",
			w.row("Remove filler words", "Drops words such as um and uh from transcripts.",
				boolSetting(w, "filler_word_removal", func(s *config.Settings) *bool { return &s.FillerWordRemoval })),
			w.row("Filler words", "", w.fillerWordsSetting()),
			w.row("Custom words", "Names and jargon spelled the way you want them.", w.customWordsSetting()),
			w.row("Word boost", "How strongly Nemotron favours your custom words while it listens. Above about 2 it starts writing them where they were not said.",
				sliderSetting(w, "custom_word_boost", func(s *config.Settings) *float64 { return &s.CustomWordBoost },
					0, config.MaxCustomWordBoost, 0.25, formatOff)),
			w.row("Correction threshold", "For models that cannot favour words (Parakeet): how much a word may differ from a custom word and still be corrected to it.",
				sliderSetting(w, "word_correction_threshold", func(s *config.Settings) *float64 { return &s.WordCorrectionThreshold },
					0, 1, 0.02, formatOff)),
		),
		group("Webhook", w.webhookRows()...),
	)
}

// WebhookActions keep the webhook's password or token in the keychain,
// under its URL. Nil SetKey leaves the field out.
type WebhookActions struct {
	// HasKey reports whether a password or token is stored for the URL.
	HasKey func(webhookURL string) (bool, error)
	// SetKey stores the password or token for the URL; "" removes it.
	SetKey func(webhookURL, secret string) error
}

// Texts of the webhook rows that tests look for.
const (
	// WebhookHint explains the webhook URL setting.
	WebhookHint = "Each finished transcription is posted to this URL as JSON, and sent again for about a day until it arrives. " +
		"Its transcription_id is the number the History page shows. Empty sends nothing: transcripts leave this computer only once you set it."
	// WebhookAuthHint explains the authentication setting.
	WebhookAuthHint = "How each POST proves it comes from you, in its Authorization header. " +
		"The password or token is kept in the system keychain under this URL, not in the settings file."
	LabelWebhookPassword = "Password"
	LabelWebhookToken    = "Token"
)

// webhookState is what the webhook rows know beyond the settings.
type webhookState struct {
	user   fyne.CanvasObject // the user name's row
	key    *keyField         // nil without WebhookActions.SetKey
	keyRow fyne.CanvasObject
}

// webhookRows are the webhook's URL and its authentication: none, Basic
// with a user name and password, or a Bearer token. Only the rows the
// authentication uses are shown.
func (w *SettingsWindow) webhookRows() []fyne.CanvasObject {
	st := &w.webhook
	authField := func(s *config.Settings) *config.WebhookAuth { return &s.WebhookAuth }
	auth := choiceSetting(w, "webhook_auth", authField, []choice[config.WebhookAuth]{
		{config.WebhookAuthNone, "None"},
		{config.WebhookAuthBasic, "Basic: user name and password"},
		{config.WebhookAuthBearer, "Bearer token"},
	}, nil)
	st.user = w.row("User name", "",
		textEntrySetting(w, "webhook_username", func(s *config.Settings) *string { return &s.WebhookUsername },
			"User name", config.CheckWebhookUsername))
	rows := []fyne.CanvasObject{
		w.row("Send transcriptions to", WebhookHint,
			textEntrySetting(w, "webhook_url", func(s *config.Settings) *string { return &s.WebhookURL },
				"https://example.com/transcriptions", config.CheckWebhookURL)),
		w.row("Authentication", WebhookAuthHint, auth),
		st.user,
	}
	if a := w.opts.Webhook; a.SetKey != nil {
		st.key = newKeyField(w, "", webhookKeyHints(LabelWebhookPassword), a.HasKey, a.SetKey)
		st.keyRow = w.row(LabelWebhookPassword, "", st.key.box)
		rows = append(rows, st.keyRow)
	}
	// Every settings change runs this, the URL's included.
	w.bind("webhook_auth", anyField(authField), auth, w.showWebhookAuth)
	return rows
}

// showWebhookAuth shows the rows the authentication in s uses, the secret's
// named for it, and whether one is stored for the URL.
func (w *SettingsWindow) showWebhookAuth(s config.Settings) {
	st := &w.webhook
	showIf(st.user, s.WebhookAuth == config.WebhookAuthBasic)
	if st.key == nil {
		return
	}
	secret := s.WebhookAuth == config.WebhookAuthBasic || s.WebhookAuth == config.WebhookAuthBearer
	showIf(st.keyRow, secret)
	if !secret {
		return
	}
	name := LabelWebhookToken
	if s.WebhookAuth == config.WebhookAuthBasic {
		name = LabelWebhookPassword
	}
	rowLabel(st.keyRow).SetText(name)
	st.key.entry.SetPlaceHolder(name)
	st.key.setHints(webhookKeyHints(name))
	st.key.show(s.WebhookURL)
}

// webhookKeyHints are what the note under the webhook's password or token
// says.
func webhookKeyHints(name string) keyHints {
	lower := strings.ToLower(name)
	return keyHints{
		stored:    fmt.Sprintf("A %s is stored for this URL. Type another to replace it.", lower),
		none:      fmt.Sprintf("No %s is stored for this URL: transcriptions wait to be sent until you store one.", lower),
		unknown:   "The keychain could not be read.",
		noAccount: "Set the URL first: the " + lower + " is stored under it.",
	}
}

// rowLabel is the label of a row that SettingsWindow.row made.
func rowLabel(row fyne.CanvasObject) *widget.Label {
	return row.(*fyne.Container).Objects[0].(*widget.Label)
}

// LaunchAtLoginHint explains the launch at login setting where Goscriber
// can launch at login.
const LaunchAtLoginHint = "Opens Goscriber when you log in, without a window, as with Start hidden."

// autostartSetting is the launch at login check box, disabled with the
// reason where there can be no login item.
func (w *SettingsWindow) autostartSetting() (*widget.Check, string) {
	c := boolSetting(w, "autostart", func(s *config.Settings) *bool { return &s.Autostart })
	if why := w.opts.Info.LoginItemUnavailable; why != "" {
		c.Disable()
		return c, sentenceCase(why) + "."
	}
	return c, LaunchAtLoginHint
}

// customWordsSetting edits the custom words.
func (w *SettingsWindow) customWordsSetting() fyne.CanvasObject {
	field := func(s *config.Settings) *[]string { return &s.CustomWords }
	l := NewWordList("Add a word or name", "No custom words.")
	l.OnChanged = func(words []string) {
		if !slices.Equal(words, w.cur.CustomWords) {
			w.change(func(s *config.Settings) { s.CustomWords = words })
		}
	}
	w.bind("custom_words", anyField(field), l, func(s config.Settings) { l.SetWords(s.CustomWords) })
	return l
}

// fillerWordsSetting edits the filler words. A null list is the built-in
// one; an empty list removes no words, which is different, so the list
// keeps the distinction: removing the last word leaves it empty, and the
// button puts the built-in list back.
func (w *SettingsWindow) fillerWordsSetting() fyne.CanvasObject {
	field := func(s *config.Settings) *[]string { return &s.CustomFillerWords }
	l := NewWordList("Add a filler word", "")
	builtIn := widget.NewButton("Use the built-in list", func() {
		if w.cur.CustomFillerWords != nil {
			w.change(func(s *config.Settings) { s.CustomFillerWords = nil })
		}
	})
	which := note("")
	l.OnChanged = func(words []string) {
		if words == nil {
			words = []string{}
		}
		w.change(func(s *config.Settings) { s.CustomFillerWords = words })
	}
	w.bind("custom_filler_words", anyField(field), l, func(s config.Settings) {
		if s.CustomFillerWords == nil {
			l.SetWords(w.opts.DefaultFillerWords)
			l.SetEmptyText("")
			which.SetText("The built-in list, as for English: um, ah and eh go from English only, and German and French have their own. " +
				"Words you add or remove make it your own list, used for every language.")
			builtIn.Disable()
		} else {
			l.SetWords(s.CustomFillerWords)
			l.SetEmptyText("No filler words: none are removed.")
			which.SetText("Your own list.")
			builtIn.Enable()
		}
	})
	return container.NewVBox(l, container.NewBorder(nil, nil, nil, builtIn, which))
}
