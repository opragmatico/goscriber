//go:build unix

package app

import (
	"syscall"
	"testing"

	"fyne.io/fyne/v2/test"
)

// SIGUSR2 starts and stops dictation, as the Wayland desktop shortcut sends
// it with pkill.
func TestToggleSignalDrivesDictation(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	out := sh.svc.output.(*testOutput)

	if err := syscall.Kill(syscall.Getpid(), toggleSignal); err != nil {
		t.Fatal(err)
	}
	q.waitUI(t, "recording", func() bool { return sh.recording })
	if err := syscall.Kill(syscall.Getpid(), toggleSignal); err != nil {
		t.Fatal(err)
	}
	q.waitUI(t, "the transcript", func() bool { return len(out.delivered()) == 1 })
	if got := out.delivered(); got[0] != "hello from the test" {
		t.Errorf("delivered %q", got)
	}
}
