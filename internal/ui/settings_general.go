package ui

import (
	"fmt"
	"slices"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/hotkey"
	"github.com/opragmatico/goscriber/internal/session"
)

// SystemDefault is how a device picker names the system's default device.
const SystemDefault = "System default"

func (w *SettingsWindow) generalPage() fyne.CanvasObject {
	transcribe := shortcutSetting(w, "transcribe_shortcut", hotkey.PurposeTranscribe, transcribeField, postProcessField, cancelField)
	postProcess := shortcutSetting(w, "post_process_shortcut", hotkey.PurposePostProcess, postProcessField, transcribeField, cancelField)
	cancel := shortcutSetting(w, "cancel_shortcut", hotkey.PurposeCancel, cancelField, transcribeField, postProcessField)

	dictation := w.row("Dictation", dictationHint, transcribe)
	postProcessRow := w.row("Post-processing", postProcessHint, postProcess)
	cancelRow := w.row("Cancel", cancelHint, cancel)
	var desktopKeys []fyne.CanvasObject
	var keysView *desktopKeysView
	if w.opts.Info.Session == session.Wayland {
		// What the hints say depends on whether the desktop's portal holds
		// the shortcuts, which the app learns after the window is built.
		keysView = w.newDesktopKeysView()
		dictation = w.row("Dictation", "", container.NewVBox(transcribe, keysView.dictationHint))
		postProcessRow = w.row("Post-processing", "", container.NewVBox(postProcess, keysView.postProcessHint))
		cancelRow = w.row("Cancel", "", container.NewVBox(cancel, keysView.cancelHint))
		desktopKeys = append(desktopKeys, keysView.row)
	}

	pg := settingsPage("General",
		group("Shortcuts", append(desktopKeys,
			dictation,
			postProcessRow,
			w.row("Activation", "How the dictation shortcut works.",
				choiceSetting(w, "activation_mode", func(s *config.Settings) *config.ActivationMode { return &s.ActivationMode },
					[]choice[config.ActivationMode]{
						{config.ActivationHoldOrToggle, "Hold or tap"},
						{config.ActivationPushToTalk, "Push to talk (hold)"},
						{config.ActivationToggle, "Toggle (tap to start and stop)"},
					}, nil)),
			w.row("Hold threshold", "With Hold or tap, holding the shortcut longer than this is push to talk; a quicker tap toggles.",
				sliderSetting(w, "hold_threshold_ms", func(s *config.Settings) *int { return &s.HoldThresholdMS },
					50, 2000, 50, formatMS)),
			cancelRow,
		)...),
		group("Microphone and sounds",
			w.row("Microphone", "", w.deviceSetting("microphone", func(s *config.Settings) *string { return &s.Microphone }, false)),
			w.row("Start and stop sounds", "A chime when recording starts and stops.",
				boolSetting(w, "audio_feedback", func(s *config.Settings) *bool { return &s.AudioFeedback })),
			w.row("Sound volume", "",
				sliderSetting(w, "audio_feedback_volume", func(s *config.Settings) *float64 { return &s.AudioFeedbackVolume },
					0, 1, 0.05, formatPercent)),
			w.row("Sound output", "Where the chimes play.",
				w.deviceSetting("output_device", func(s *config.Settings) *string { return &s.OutputDevice }, true)),
			w.row("Mute while recording", "Silences other audio while you dictate, then puts it back (macOS).",
				boolSetting(w, "mute_while_recording", func(s *config.Settings) *bool { return &s.MuteWhileRecording })),
		),
		group("Overlay",
			w.row("Overlay", "The pill that shows Goscriber is listening, with the words so far when live.",
				choiceSetting(w, "overlay_style", func(s *config.Settings) *config.OverlayStyle { return &s.OverlayStyle },
					[]choice[config.OverlayStyle]{
						{config.OverlayLive, "Live text"},
						{config.OverlayMinimal, "Minimal"},
						{config.OverlayNone, "None"},
					}, nil)),
			w.row("Position", "",
				choiceSetting(w, "overlay_position", func(s *config.Settings) *config.OverlayPosition { return &s.OverlayPosition },
					[]choice[config.OverlayPosition]{
						{config.OverlayBottom, "Bottom of the screen"},
						{config.OverlayTop, "Top of the screen"},
					}, nil)),
		),
	)
	if keysView != nil {
		keysView.page = pg.(*container.Scroll).Content
	}
	return pg
}

// shortcutField is one of the shortcut settings, and how a refusal names
// it.
type shortcutField struct {
	name  string
	field func(*config.Settings) *string
}

var (
	transcribeField  = shortcutField{"dictation", func(s *config.Settings) *string { return &s.TranscribeShortcut }}
	postProcessField = shortcutField{"post-processing", func(s *config.Settings) *string { return &s.PostProcessShortcut }}
	cancelField      = shortcutField{"cancel", func(s *config.Settings) *string { return &s.CancelShortcut }}
)

// shortcutSetting is a shortcut recorder bound to a shortcut field, with a
// line for why a combination was refused and a button that puts the
// default back. others are the other shortcuts, whose keys this one may
// not take. The post-processing shortcut may also be none, which its
// clear button sets.
func shortcutSetting(w *SettingsWindow, key string, purpose hotkey.Purpose, setting shortcutField, others ...shortcutField) fyne.CanvasObject {
	field := setting.field
	in := NewShortcutInput(purpose)
	problem := widget.NewLabel("")
	problem.Importance = widget.DangerImportance
	problem.Wrapping = fyne.TextWrapWord
	problem.Hide()
	in.OnError = func(msg string) {
		problem.SetText(msg)
		problem.Hidden = msg == ""
		problem.Refresh()
	}
	in.Check = func(b hotkey.Binding) error {
		for _, o := range others {
			if ob, err := hotkey.Parse(*o.field(&w.cur)); err == nil && ob == b {
				return fmt.Errorf("the %s shortcut already uses it", o.name)
			}
		}
		return nil
	}
	in.OnChanged = func(s string) { setField(w, field, s) }
	in.OnRecording = func(on bool) {
		if w.opts.OnRecordShortcut != nil {
			w.opts.OnRecordShortcut(on)
		}
	}
	defaults := config.Defaults()
	def := *field(&defaults)
	reset := widget.NewButtonWithIcon("", theme.ContentUndoIcon(), func() {
		in.OnError("")
		setField(w, field, def)
	})
	buttons := container.NewHBox(reset)
	var clearBtn *widget.Button
	if purpose == hotkey.PurposePostProcess {
		clearBtn = widget.NewButtonWithIcon("", theme.ContentClearIcon(), func() {
			in.OnError("")
			setField(w, field, "")
		})
		buttons.Objects = []fyne.CanvasObject{clearBtn, reset}
	}
	w.bind(key, anyField(field), in, func(s config.Settings) {
		cur := *field(&s)
		in.SetShortcut(cur)
		enable(reset, cur != def)
		if clearBtn != nil {
			enable(clearBtn, cur != "")
		}
	})
	return container.NewVBox(container.NewBorder(nil, nil, nil, buttons, in), problem)
}

// enable enables or disables b.
func enable(b *widget.Button, on bool) {
	if on {
		b.Enable()
	} else {
		b.Disable()
	}
}

// deviceState is what the device pickers offer.
type deviceState struct {
	inputs, outputs []string
	listed          bool
	bindings        []*binding
}

// deviceSetting is a picker of the system default and the audio devices,
// bound to a device name field. A device that is not connected stays
// selected, marked as such: settings keep it for when it comes back.
func (w *SettingsWindow) deviceSetting(key string, field func(*config.Settings) *string, output bool) *widget.Select {
	sel := widget.NewSelect(nil, nil)
	var names []string // the devices behind sel's options after the first
	sel.OnChanged = func(string) {
		i := sel.SelectedIndex()
		switch {
		case i == 0:
			setField(w, field, "")
		case i > 0 && i <= len(names):
			setField(w, field, names[i-1])
		}
	}
	w.bind(key, anyField(field), sel, func(s config.Settings) {
		cur := *field(&s)
		names = w.devices.inputs
		if output {
			names = w.devices.outputs
		}
		labels := append([]string{SystemDefault}, names...)
		selected := SystemDefault
		switch {
		case cur == "":
		case slices.Contains(names, cur):
			selected = cur
		default:
			names = append(append([]string(nil), names...), cur)
			selected = cur
			if w.devices.listed {
				selected = fmt.Sprintf("%s (not connected)", cur)
			}
			labels = append(labels, selected)
		}
		sel.SetOptions(labels)
		sel.SetSelected(selected)
	})
	w.devices.bindings = append(w.devices.bindings, w.bindings[len(w.bindings)-1])
	return sel
}

// refreshDevices lists the audio devices in the background, then offers
// them in the pickers.
func (w *SettingsWindow) refreshDevices() {
	list := w.opts.Devices
	if list == nil {
		list = systemDevices
	}
	go func() {
		in, out, err := list()
		w.do(func() {
			if err != nil {
				w.log().Warn("the audio devices could not be listed", "err", err)
			}
			w.devices.inputs, w.devices.outputs, w.devices.listed = deviceNames(in), deviceNames(out), err == nil
			for _, b := range w.devices.bindings {
				w.loadBinding(b)
			}
		})
	}()
}

func systemDevices() (inputs, outputs []audio.Device, err error) {
	inputs, err = audio.InputDevices()
	if err != nil {
		return nil, nil, err
	}
	outputs, err = audio.OutputDevices()
	return inputs, outputs, err
}

func deviceNames(devs []audio.Device) []string {
	names := make([]string, 0, len(devs))
	for _, d := range devs {
		if !slices.Contains(names, d.Name) {
			names = append(names, d.Name)
		}
	}
	return names
}
