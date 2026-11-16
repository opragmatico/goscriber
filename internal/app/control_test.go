package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/opragmatico/goscriber/internal/instance"
	"github.com/opragmatico/goscriber/internal/ui"
)

// sendCommand sends c to the app whose config directory is home, as
// another launch of goscriber would.
func sendCommand(t *testing.T, home string, c instance.Command) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := instance.Send(ctx, home, c); err != nil {
		t.Fatalf("Send(%s): %v", c, err)
	}
}

// goscriber --toggle-transcription and --cancel drive dictation in the
// running app through its control socket, and a second launch brings up the
// settings window.
func TestRemoteControlDrivesDictation(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m"}`)
	inst, err := instance.Acquire(home)
	if errors.Is(err, instance.ErrUnsupported) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inst.Close() })
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{Instance: inst})
	out := sh.svc.output.(*testOutput)

	sendCommand(t, home, instance.Toggle)
	q.waitUI(t, "the recording icon", func() bool { return a.trayIcon == ui.TrayIcon(ui.TrayRecording) })
	sendCommand(t, home, instance.Toggle)
	q.waitUI(t, "the transcript", func() bool { return len(out.delivered()) == 1 })
	q.waitUI(t, "the idle icon", func() bool { return a.trayIcon == ui.TrayIcon(ui.TrayIdle) })

	sendCommand(t, home, instance.Toggle)
	q.waitUI(t, "the recording icon", func() bool { return a.trayIcon == ui.TrayIcon(ui.TrayRecording) })
	sendCommand(t, home, instance.Cancel)
	q.waitUI(t, "the idle icon", func() bool { return a.trayIcon == ui.TrayIcon(ui.TrayIdle) })
	time.Sleep(50 * time.Millisecond)
	if got := out.delivered(); len(got) != 1 {
		t.Errorf("delivered %q after a cancel", got)
	}

	if sh.settings.Visible() {
		t.Fatal("the settings window is open before anyone asked")
	}
	sendCommand(t, home, instance.Show)
	q.waitUI(t, "the settings window", func() bool { return sh.settings.Visible() })
	if p := sh.settings.Page(); p != ui.PageGeneral {
		t.Errorf("the window opened on %v, want General", p)
	}
}

// A second launch raises the window as it is, and while onboarding is not
// over opens it there, as a launch would.
func TestSecondLaunchShowsTheWindowAsItIs(t *testing.T) {
	isolate(t)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newFirstLaunchShell(t, a, Options{StartHidden: true})
	q.drain()
	if sh.settings.Visible() {
		t.Fatal("starting hidden with a tray opened the window")
	}
	if err := sh.control(instance.Show); err != nil {
		t.Fatal(err)
	}
	q.waitUI(t, "onboarding", func() bool { return sh.settings.Visible() })
	if step, on := sh.settings.Onboarding(); !on || step != ui.StepWelcome {
		t.Fatalf("onboarding %v on %v, want its welcome", on, step)
	}

	sh.settings.Show(ui.PageModels)
	if err := sh.control(instance.Show); err != nil {
		t.Fatal(err)
	}
	q.drain()
	if _, on := sh.settings.Onboarding(); on || sh.settings.Page() != ui.PageModels || !sh.settings.Visible() {
		t.Errorf("the open window changed: page %v, onboarding %v", sh.settings.Page(), on)
	}
}

// Commands that arrive once the app is shutting down are refused, so the
// command line says why instead of reporting success.
func TestRemoteControlRefusedWhileQuitting(t *testing.T) {
	isolate(t)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, _ := newTestShell(t, a, Options{})
	sh.stopFollowing()
	if err := sh.control(instance.Toggle); !errors.Is(err, errQuitting) {
		t.Errorf("control after quit: %v, want errQuitting", err)
	}
}
