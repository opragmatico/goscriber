package ui

import (
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/hotkey"
)

// press records keys on a shortcut input: modifiers held, then the key.
func press(in *ShortcutInput, keys ...fyne.KeyName) {
	for _, k := range keys {
		in.KeyDown(&fyne.KeyEvent{Name: k})
	}
	for _, k := range slices.Backward(keys) {
		in.KeyUp(&fyne.KeyEvent{Name: k})
	}
}

// shortcutProblem is the line under a shortcut input that says why keys
// were refused, or "" when it is hidden.
func (tw *testWindow) shortcutProblem(in *ShortcutInput) string {
	for _, o := range tw.pageObjects(PageGeneral) {
		if box, ok := o.(*fyne.Container); ok && len(box.Objects) == 2 {
			if row, ok := box.Objects[0].(*fyne.Container); ok && slices.Contains(row.Objects, fyne.CanvasObject(in)) {
				if l := box.Objects[1].(*widget.Label); l.Visible() {
					return l.Text
				}
				return ""
			}
		}
	}
	return "(no problem line)"
}

func TestGeneralPageShortcutRecorder(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.show(PageGeneral)
	in := tw.bound(t, "transcribe_shortcut").(*ShortcutInput)
	if in.Text != ShortcutLabel(config.Defaults().TranscribeShortcut) {
		t.Errorf("shows %q", in.Text)
	}

	test.Tap(in)
	if !in.Recording() || in.Text != ShortcutPrompt || !slices.Equal(tw.recording, []bool{true}) {
		t.Fatalf("recording %v, text %q, told %v", in.Recording(), in.Text, tw.recording)
	}
	// A plain letter would be swallowed in every app: refused, and still
	// listening.
	press(in, fyne.KeyK)
	if !in.Recording() || !strings.Contains(tw.shortcutProblem(in), "add a modifier") {
		t.Errorf("recording %v, problem %q", in.Recording(), tw.shortcutProblem(in))
	}
	press(in, "Pause")
	if !strings.Contains(tw.shortcutProblem(in), "cannot be part of a shortcut") {
		t.Errorf("problem %q", tw.shortcutProblem(in))
	}
	in.KeyDown(&fyne.KeyEvent{Name: "LeftControl"})
	in.KeyDown(&fyne.KeyEvent{Name: "LeftShift"})
	if in.Text != modifiersLabel(hotkey.ModCtrl|hotkey.ModShift)+"…" {
		t.Errorf("while holding the modifiers: %q", in.Text)
	}
	in.KeyDown(&fyne.KeyEvent{Name: fyne.KeyK})
	if in.Recording() || !slices.Equal(tw.recording, []bool{true, false}) {
		t.Errorf("recording %v, told %v", in.Recording(), tw.recording)
	}
	if got := tw.store.Get().TranscribeShortcut; got != "ctrl+shift+k" {
		t.Errorf("saved %q", got)
	}
	if in.Text != ShortcutLabel("ctrl+shift+k") || tw.shortcutProblem(in) != "" {
		t.Errorf("shows %q, problem %q", in.Text, tw.shortcutProblem(in))
	}

	// Escape on its own stops listening without a change.
	test.Tap(in)
	press(in, fyne.KeyEscape)
	if in.Recording() || tw.store.Get().TranscribeShortcut != "ctrl+shift+k" {
		t.Errorf("Escape: recording %v, saved %q", in.Recording(), tw.store.Get().TranscribeShortcut)
	}
	// Clicking elsewhere does too, and so does closing the window.
	test.Tap(in)
	tw.Window().Canvas().Unfocus()
	if in.Recording() {
		t.Error("still listening after losing the keyboard")
	}
	test.Tap(in)
	tw.closeRequested()
	if in.Recording() || tw.recording[len(tw.recording)-1] {
		t.Errorf("still listening after the window closed: told %v", tw.recording)
	}
	tw.show(PageGeneral)

	// The cancel shortcut may be a plain Escape, but not the dictation one.
	cancel := tw.bound(t, "cancel_shortcut").(*ShortcutInput)
	test.Tap(cancel)
	press(cancel, "LeftControl", "LeftShift", fyne.KeyK)
	if !cancel.Recording() || tw.shortcutProblem(cancel) != "the dictation shortcut already uses it" {
		t.Errorf("recording %v, problem %q", cancel.Recording(), tw.shortcutProblem(cancel))
	}
	press(cancel, "LeftShift", fyne.KeyEscape)
	if got := tw.store.Get().CancelShortcut; got != "shift+escape" {
		t.Errorf("cancel saved %q", got)
	}

	// The undo button puts the default back.
	var undo []*widget.Button
	for _, o := range tw.pageObjects(PageGeneral) {
		if b, ok := o.(*widget.Button); ok && b.Icon == theme.ContentUndoIcon() && !b.Disabled() {
			undo = append(undo, b)
		}
	}
	if len(undo) != 2 {
		t.Fatalf("%d enabled reset buttons, want one per changed shortcut", len(undo))
	}
	for _, b := range undo {
		test.Tap(b)
	}
	if s := tw.store.Get(); s.TranscribeShortcut != config.Defaults().TranscribeShortcut || s.CancelShortcut != "escape" {
		t.Errorf("after reset: %q, %q", s.TranscribeShortcut, s.CancelShortcut)
	}
}

// The post-processing shortcut is recorded as the others are, may not take
// their keys, and may be none.
func TestGeneralPagePostProcessShortcut(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.show(PageGeneral)
	def := config.Defaults()
	in := tw.bound(t, "post_process_shortcut").(*ShortcutInput)
	if in.Text != ShortcutLabel(def.PostProcessShortcut) {
		t.Errorf("shows %q", in.Text)
	}

	// The dictation shortcut's keys are refused, and so are the cancel
	// shortcut's.
	transcribe, err := hotkey.Parse(def.TranscribeShortcut)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[hotkey.Modifier]fyne.KeyName{hotkey.ModCtrl: "LeftControl", hotkey.ModAlt: "LeftAlt", hotkey.ModShift: "LeftShift", hotkey.ModSuper: "LeftSuper"}
	var held []fyne.KeyName
	for _, m := range []hotkey.Modifier{hotkey.ModCtrl, hotkey.ModAlt, hotkey.ModShift, hotkey.ModSuper} {
		if transcribe.Has(m) {
			held = append(held, keys[m])
		}
	}
	test.Tap(in)
	press(in, append(held, fyne.KeySpace)...)
	if !in.Recording() || tw.shortcutProblem(in) != "the dictation shortcut already uses it" {
		t.Errorf("dictation's keys: recording %v, problem %q", in.Recording(), tw.shortcutProblem(in))
	}
	if err := tw.store.Update(func(s *config.Settings) { s.CancelShortcut = "ctrl+shift+k" }); err != nil {
		t.Fatal(err)
	}
	tw.SetSettings(tw.store.Get())
	press(in, "LeftControl", "LeftShift", fyne.KeyK)
	if !in.Recording() || tw.shortcutProblem(in) != "the cancel shortcut already uses it" {
		t.Errorf("cancel's keys: recording %v, problem %q", in.Recording(), tw.shortcutProblem(in))
	}
	press(in, "LeftControl", "LeftShift", fyne.KeyP)
	if got := tw.store.Get().PostProcessShortcut; got != "ctrl+shift+p" || in.Recording() {
		t.Errorf("saved %q, recording %v", got, in.Recording())
	}

	// The clear button sets none, which the reset button undoes.
	clearBtn, reset := tw.shortcutButtons(t, in)
	test.Tap(clearBtn)
	if got := tw.store.Get().PostProcessShortcut; got != "" || in.Text != "None" || !clearBtn.Disabled() || reset.Disabled() {
		t.Errorf("after clearing: saved %q, shows %q, clear enabled %v, reset enabled %v", got, in.Text, !clearBtn.Disabled(), !reset.Disabled())
	}
	test.Tap(reset)
	if got := tw.store.Get().PostProcessShortcut; got != def.PostProcessShortcut || clearBtn.Disabled() || !reset.Disabled() {
		t.Errorf("after the reset: saved %q, clear enabled %v, reset enabled %v", got, !clearBtn.Disabled(), !reset.Disabled())
	}
	// The other shortcuts cannot be none.
	if c, _ := tw.shortcutButtons(t, tw.bound(t, "transcribe_shortcut").(*ShortcutInput)); c != nil {
		t.Error("the dictation shortcut has a clear button")
	}
}

// shortcutButtons are the clear button, or nil, and the reset button
// beside a shortcut input.
func (tw *testWindow) shortcutButtons(t *testing.T, in *ShortcutInput) (clearBtn, reset *widget.Button) {
	t.Helper()
	for _, o := range tw.pageObjects(PageGeneral) {
		row, ok := o.(*fyne.Container)
		if !ok || !slices.Contains(row.Objects, fyne.CanvasObject(in)) {
			continue
		}
		for _, side := range row.Objects {
			box, ok := side.(*fyne.Container)
			if !ok {
				continue
			}
			for _, b := range box.Objects {
				switch b := b.(*widget.Button); b.Icon {
				case theme.ContentClearIcon():
					clearBtn = b
				case theme.ContentUndoIcon():
					reset = b
				}
			}
		}
	}
	if reset == nil {
		t.Fatal("no reset button beside the shortcut")
	}
	return clearBtn, reset
}

func TestGeneralPageDevicesAndChoices(t *testing.T) {
	tw := newTestWindow(t, nil)
	if err := tw.store.Update(func(s *config.Settings) { s.Microphone = "Old Headset" }); err != nil {
		t.Fatal(err)
	}
	tw.SetSettings(tw.store.Get())
	tw.show(PageGeneral)

	mic := tw.bound(t, "microphone").(*widget.Select)
	want := []string{SystemDefault, "MacBook Pro Microphone", "USB Mic", "Old Headset (not connected)"}
	if !slices.Equal(mic.Options, want) || mic.Selected != "Old Headset (not connected)" {
		t.Errorf("microphones %q, selected %q", mic.Options, mic.Selected)
	}
	mic.SetSelected(SystemDefault)
	if got := tw.store.Get().Microphone; got != "" {
		t.Errorf("system default saved as %q", got)
	}
	if !slices.Equal(mic.Options, want[:3]) {
		t.Errorf("a device no longer selected is still offered: %q", mic.Options)
	}
	out := tw.bound(t, "output_device").(*widget.Select)
	if !slices.Equal(out.Options, []string{SystemDefault, "MacBook Pro Speakers"}) || out.Selected != SystemDefault {
		t.Errorf("outputs %q, selected %q", out.Options, out.Selected)
	}
	out.SetSelected("MacBook Pro Speakers")

	tw.bound(t, "activation_mode").(*widget.Select).SetSelected("Push to talk (hold)")
	tw.bound(t, "overlay_style").(*widget.Select).SetSelected("Minimal")
	tw.bound(t, "overlay_position").(*widget.Select).SetSelected("Top of the screen")
	test.Tap(tw.bound(t, "audio_feedback").(*widget.Check))
	volume := tw.bound(t, "audio_feedback_volume").(*settingSlider)
	volume.slider.SetValue(0.35)
	test.Tap(tw.bound(t, "mute_while_recording").(*widget.Check))

	s := tw.reopen(t)
	if s.OutputDevice != "MacBook Pro Speakers" || s.ActivationMode != config.ActivationPushToTalk ||
		s.OverlayStyle != config.OverlayMinimal || s.OverlayPosition != config.OverlayTop ||
		!s.AudioFeedback || s.AudioFeedbackVolume != 0.35 || !s.MuteWhileRecording {
		t.Errorf("saved %+v", s)
	}
	if volume.value.Text != "35 %" {
		t.Errorf("volume label %q", volume.value.Text)
	}
}

func TestShortcutLabel(t *testing.T) {
	for _, tt := range []struct{ in, goos, want string }{
		{"option+space", "darwin", "⌥ Space"},
		{"ctrl+shift+cmd+d", "darwin", "⌃⇧⌘ D"},
		{"f5", "darwin", "F5"},
		{"escape", "darwin", "Esc"},
		{"ctrl+alt+space", "linux", "Ctrl+Alt+Space"},
		{"super+up", "windows", "Win+↑"},
		{"shift+escape", "linux", "Shift+Esc"},
		{"", "linux", "None"},
		{"hyper+q", "linux", "hyper+q"},
	} {
		if got := shortcutLabel(tt.in, tt.goos); got != tt.want {
			t.Errorf("shortcutLabel(%q, %s) = %q, want %q", tt.in, tt.goos, got, tt.want)
		}
	}
}

func TestWordList(t *testing.T) {
	l := NewWordList("Add a word", "No words.")
	var got [][]string
	l.OnChanged = func(words []string) { got = append(got, words) }
	l.Type("  NeMo ,  Kowalczyk  Jr ,nemo, ")
	if !slices.Equal(l.Words(), []string{"NeMo", "Kowalczyk Jr"}) {
		t.Errorf("words %q", l.Words())
	}
	if l.empty.Visible() || len(l.chips.Objects) != 2 {
		t.Errorf("empty label visible %v, %d chips", l.empty.Visible(), len(l.chips.Objects))
	}
	l.Type("") // nothing new: no change reported
	test.Tap(l.chips.Objects[0].(*widget.Button))
	if !slices.Equal(l.Words(), []string{"Kowalczyk Jr"}) {
		t.Errorf("after removing NeMo: %q", l.Words())
	}
	if len(got) != 2 {
		t.Errorf("changes reported %q", got)
	}
	l.SetWords(nil)
	if !l.empty.Visible() || len(got) != 2 {
		t.Error("SetWords should show the empty text and report nothing")
	}
}
