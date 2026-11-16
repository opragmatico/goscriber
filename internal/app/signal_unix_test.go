//go:build unix

package app

import (
	"syscall"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/opragmatico/goscriber/internal/ui"
)

// SIGUSR2 toggles dictation, as in Handy.
func TestSIGUSR2TogglesDictation(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	out := sh.svc.output.(*testOutput)

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGUSR2); err != nil {
		t.Fatal(err)
	}
	q.waitUI(t, "the recording icon", func() bool { return a.trayIcon == ui.TrayIcon(ui.TrayRecording) })
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGUSR2); err != nil {
		t.Fatal(err)
	}
	q.waitUI(t, "the transcript", func() bool { return len(out.delivered()) == 1 })
}
