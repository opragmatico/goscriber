//go:build linux

package platform

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/output"
	"github.com/opragmatico/goscriber/internal/platform/x11test"
)

// These tests need an X server: run them under Xvfb (xvfb-run go test).

func needX11(t *testing.T) {
	t.Helper()
	if os.Getenv("DISPLAY") == "" {
		t.Skip("no X server: set DISPLAY, for example with xvfb-run")
	}
	t.Setenv("XDG_SESSION_TYPE", "x11")
	t.Setenv("WAYLAND_DISPLAY", "")
}

func startApp(t *testing.T, focus bool) *x11test.App {
	t.Helper()
	a, err := x11test.Start(focus)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func selectionText(t *testing.T, name string) (string, selection) {
	t.Helper()
	text, held, err := readSelection(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return text, held
}

// A transcript goes on CLIPBOARD and PRIMARY, which other apps read from
// Goscriber, marked so clipboard managers leave it out when transient.
func TestX11SetText(t *testing.T) {
	needX11(t)
	var pb Pasteboard
	if err := pb.SetText("Größe, 😀", true); err != nil {
		t.Fatal(err)
	}
	for _, sel := range []string{"CLIPBOARD", "PRIMARY"} {
		if text, held := selectionText(t, sel); held != selText || text != "Größe, 😀" {
			t.Errorf("%s holds %q (%v)", sel, text, held)
		}
	}
	other := startApp(t, false)
	targets := other.Targets("CLIPBOARD")
	for _, want := range []string{"UTF8_STRING", "TARGETS", "x-kde-passwordManagerHint"} {
		if !slices.Contains(targets, want) {
			t.Errorf("transient text offers %q, without %s", targets, want)
		}
	}
	if err := pb.SetText("keep me", false); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(other.Targets("CLIPBOARD"), "x-kde-passwordManagerHint") {
		t.Error("text that stays on the clipboard is marked for clipboard managers to skip")
	}
	if text, ok := pb.Text(); !ok || text != "keep me" {
		t.Errorf("Text = %q, %v", text, ok)
	}
}

// ChangeCount moves when Goscriber writes and when another app copies.
func TestX11ChangeCount(t *testing.T) {
	needX11(t)
	var pb Pasteboard
	if err := pb.SetText("mine", false); err != nil {
		t.Fatal(err)
	}
	mine := pb.ChangeCount()
	startApp(t, false).Copy("CLIPBOARD", "theirs")
	if !x11test.Wait(2*time.Second, func() bool { return pb.ChangeCount() != mine }) {
		t.Error("ChangeCount did not move when another app copied")
	}
	if text, _ := pb.Text(); text != "theirs" {
		t.Errorf("Text = %q, want the other app's copy", text)
	}
}

// Save keeps the text of both selections; restoring puts it back, held by
// Goscriber, and an empty clipboard is restored empty.
func TestX11SaveRestore(t *testing.T) {
	needX11(t)
	var pb Pasteboard
	other := startApp(t, false)
	other.Copy("CLIPBOARD", "the user's copy")
	other.Copy("PRIMARY", "a selection")
	restore, err := pb.Save()
	if err != nil {
		t.Fatal(err)
	}
	if err := pb.SetText("transcript", true); err != nil {
		t.Fatal(err)
	}
	if text, _ := selectionText(t, "PRIMARY"); text != "transcript" {
		t.Fatalf("PRIMARY = %q during the paste", text)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if text, held := selectionText(t, "CLIPBOARD"); held != selText || text != "the user's copy" {
		t.Errorf("CLIPBOARD after restore = %q (%v)", text, held)
	}
	if text, held := selectionText(t, "PRIMARY"); held != selText || text != "a selection" {
		t.Errorf("PRIMARY after restore = %q (%v)", text, held)
	}

	// Nothing on the clipboard: it is empty again afterwards.
	if err := pb.SetText("", false); err != nil {
		t.Fatal(err)
	}
	if _, held := selectionText(t, "CLIPBOARD"); held != selEmpty {
		t.Fatalf("SetText(\"\") left CLIPBOARD %v", held)
	}
	restore, err = pb.Save()
	if err != nil {
		t.Fatal(err)
	}
	_ = pb.SetText("transcript", true)
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if _, held := selectionText(t, "CLIPBOARD"); held != selEmpty {
		t.Errorf("an empty clipboard came back %v", held)
	}
}

// A selection the user makes during the paste is theirs: PRIMARY is put
// back only while Goscriber still holds it.
func TestX11RestoreKeepsANewSelection(t *testing.T) {
	needX11(t)
	var pb Pasteboard
	other := startApp(t, false)
	other.Copy("PRIMARY", "old selection")
	restore, err := pb.Save()
	if err != nil {
		t.Fatal(err)
	}
	_ = pb.SetText("transcript", true)
	other.Copy("PRIMARY", "new selection")
	x11test.Wait(time.Second, func() bool { t, _, _ := readSelection("PRIMARY"); return t == "new selection" })
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if text, _ := selectionText(t, "PRIMARY"); text != "new selection" {
		t.Errorf("PRIMARY = %q, want the selection made during the paste", text)
	}
}

func xtestKeyboard(keys config.PasteKeys) Keyboard {
	return Keyboard{Settings: func() config.Settings {
		s := config.Defaults()
		s.TypingTool = config.TypingAuto
		s.PasteKeys = keys
		return s
	}}
}

// The whole paste, as dictation does it, into an app that pastes on the
// paste keys: the app gets the transcript and the clipboard is the user's
// again afterwards.
func TestX11PasteIntoAnApp(t *testing.T) {
	needX11(t)
	if err := xtestAvailable(); err != nil {
		t.Skip(err)
	}
	for _, keys := range []config.PasteKeys{config.PasteShiftInsert, config.PasteCtrlV} {
		t.Run(string(keys), func(t *testing.T) {
			app := startApp(t, true)
			app.Copy("CLIPBOARD", "the user's copy")
			out := output.New(Pasteboard{}, xtestKeyboard(keys), nil)
			err := out.Deliver(context.Background(), "Ask not what your country can do.", output.Options{
				Method:      config.PasteClipboard,
				Clipboard:   config.ClipboardRestore,
				DelayBefore: 20 * time.Millisecond,
				DelayAfter:  300 * time.Millisecond,
				AutoSubmit:  true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !x11test.Wait(2*time.Second, func() bool { return len(app.Pasted()) > 0 }) {
				t.Fatalf("the app pasted nothing; it got keys %+v", app.Keys())
			}
			if got := app.Pasted(); got[0] != "Ask not what your country can do." {
				t.Errorf("the app pasted %q", got)
			}
			if !x11test.Wait(time.Second, func() bool { k := app.Keys(); return len(k) > 0 && k[len(k)-1].Sym == x11test.KeyReturn }) {
				t.Errorf("no Return after the paste: keys %+v", app.Keys())
			}
			if text, _ := (Pasteboard{}).Text(); text != "the user's copy" {
				t.Errorf("the clipboard holds %q after the paste", text)
			}
		})
	}
}

// Typing goes through keys of the layout, with Shift where needed, and
// characters the layout lacks through a key mapped for the moment.
func TestX11Type(t *testing.T) {
	needX11(t)
	if err := xtestAvailable(); err != nil {
		t.Skip(err)
	}
	app := startApp(t, true)
	text := "Hello, World! 42 (é) — €, ü\nok 😀"
	if err := xtestKeyboard(config.PasteCtrlV).Type(text); err != nil {
		t.Fatal(err)
	}
	if !x11test.Wait(3*time.Second, func() bool { return app.Typed() == text }) {
		t.Errorf("typed %q, want %q", app.Typed(), text)
	}
}

// Held modifiers do not leak into the paste keys: with Ctrl and Alt still
// down from the shortcut, Shift+Insert arrives as Shift+Insert.
func TestX11PasteKeysReleaseHeldModifiers(t *testing.T) {
	needX11(t)
	if err := xtestAvailable(); err != nil {
		t.Skip(err)
	}
	const altL = 0xffe9
	app := startApp(t, true)
	app.Hold(xkControlL)
	app.Hold(altL)
	t.Cleanup(func() { app.Release(altL); app.Release(xkControlL) })
	if err := xtestKeyboard(config.PasteShiftInsert).Paste(); err != nil {
		t.Fatal(err)
	}
	if !x11test.Wait(2*time.Second, func() bool { return len(app.Keys()) >= 1 }) {
		t.Fatalf("keys %+v", app.Keys())
	}
	k := app.Keys()[0]
	if k.Sym != x11test.KeyInsert || k.State != x11test.ShiftMask {
		t.Errorf("the paste keys arrived as keysym %#x with state %#x, want Insert with Shift alone", k.Sym, k.State)
	}
}
