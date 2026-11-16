package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/hotkey"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/session"
	"github.com/opragmatico/goscriber/internal/ui"
)

// useWayland makes the next shell think it runs in a Wayland session.
func useWayland(t *testing.T) {
	t.Helper()
	prev := currentSession
	currentSession = func() session.Kind { return session.Wayland }
	t.Cleanup(func() { currentSession = prev })
}

// fakePortal stands in for the desktop's global shortcuts portal.
type fakePortal struct {
	fakeShortcuts
	opts       chan desktopShortcutsOptions
	configured atomic.Int32
	closed     atomic.Bool
}

func (p *fakePortal) Configure() error {
	p.configured.Add(1)
	return nil
}

func (p *fakePortal) Close() error {
	p.closed.Store(true)
	return nil
}

// usePortal makes opening the desktop's portal return p, or fail with err.
func usePortal(t *testing.T, p *fakePortal, err error) {
	t.Helper()
	prev := openDesktopShortcuts
	openDesktopShortcuts = func(_ context.Context, o desktopShortcutsOptions) (desktopShortcuts, error) {
		if err != nil {
			return nil, err
		}
		p.opts <- o
		return p, nil
	}
	t.Cleanup(func() { openDesktopShortcuts = prev })
}

// shown is shortcut as this OS formats it: Alt is Option on macOS.
func shown(t *testing.T, shortcut string) string {
	t.Helper()
	b, err := hotkey.Parse(shortcut)
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// windowText is the text of the labels and buttons on screen in w,
// leaving out what is hidden, along with everything inside it.
func windowText(w fyne.Window) string {
	var b strings.Builder
	var walk func(o fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		if !o.Visible() {
			return
		}
		switch x := o.(type) {
		case *widget.Label:
			b.WriteString(x.Text + "\n")
		case *widget.Button:
			b.WriteString(x.Text + "\n")
		case *fyne.Container:
			for _, c := range x.Objects {
				walk(c)
			}
		case fyne.Widget:
			for _, c := range test.WidgetRenderer(x).Objects() {
				walk(c)
			}
		}
	}
	walk(w.Content())
	return b.String()
}

// button is the visible button labelled text in w, or nil.
func button(w fyne.Window, text string) *widget.Button {
	for _, o := range test.LaidOutObjects(w.Content()) {
		if b, ok := o.(*widget.Button); ok && b.Text == text && b.Visible() {
			return b
		}
	}
	return nil
}

func TestWaylandShortcutsThroughTheDesktopPortal(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	useWayland(t)
	p := &fakePortal{opts: make(chan desktopShortcutsOptions, 1)}
	usePortal(t, p, nil)
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m", "transcribe_shortcut": "ctrl+alt+space", "cancel_shortcut": "escape"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})

	var o desktopShortcutsOptions
	select {
	case o = <-p.opts:
	case <-time.After(3 * time.Second):
		t.Fatal("the shell never asked the desktop's portal")
	}
	if tr, c := o.Suggest(); tr != "ctrl+alt+space" || c != "escape" {
		t.Errorf("suggested %q and %q", tr, c)
	}
	// The portal registers the shortcut; the system's backend is not asked.
	p.waitLog(t, "+"+shown(t, "ctrl+alt+space"))
	testShortcuts.waitLog(t)

	sh.settings.Show(ui.PageGeneral)
	o.OnStatus(ui.DesktopShortcuts{State: ui.PortalAsking})
	q.runNext(t)
	if got := windowText(sh.settings.Window()); !strings.Contains(got, "Waiting for you to confirm") || !strings.Contains(got, ui.DictationHintPortal) {
		t.Errorf("General page while the desktop asks:\n%s", got)
	}

	o.OnStatus(ui.DesktopShortcuts{State: ui.PortalBound, Transcribe: "Ctrl+Alt+Space", CanChange: true})
	q.runNext(t)
	got := windowText(sh.settings.Window())
	if !strings.Contains(got, "Dictation: Ctrl+Alt+Space. Cancel: no keys.") || strings.Contains(got, ui.ToggleCommand) {
		t.Errorf("General page once bound:\n%s", got)
	}
	change := button(sh.settings.Window(), "Change…")
	if change == nil {
		t.Fatal("no Change… button")
	}
	test.Tap(change)
	if p.configured.Load() != 1 {
		t.Error("Change… did not open the desktop's dialog")
	}

	// A dismissed dialog is a problem to fix, until the desktop binds.
	o.OnStatus(ui.DesktopShortcuts{State: ui.PortalRefused, Problem: "dismissed"})
	q.waitUI(t, "the shortcut notice", func() bool { return sh.notices.Count(notice.CodeShortcutFailed) == 1 })
	if n := sh.notices.Active(); len(n) == 0 || !strings.Contains(n[0].Message, ui.ToggleCommand) {
		t.Errorf("notices %+v", n)
	}
	o.OnStatus(ui.DesktopShortcuts{State: ui.PortalBound, Transcribe: "Ctrl+Alt+Space"})
	q.waitUI(t, "the notice to clear", func() bool { return sh.notices.Count(notice.CodeShortcutFailed) == 0 })

	sh.stopFollowing()
	if !p.closed.Load() {
		t.Error("quitting left the portal session open")
	}
}

func TestWaylandWithoutThePortal(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	useWayland(t)
	usePortal(t, nil, errors.New("no portal"))
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m", "transcribe_shortcut": "ctrl+alt+space"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, _ := newTestShell(t, a, Options{})

	// The system's backend takes the shortcut, for X11 windows, and the
	// desktop's own shortcut runs the toggle command.
	testShortcuts.waitLog(t, "+"+shown(t, "ctrl+alt+space"))
	sh.settings.Show(ui.PageGeneral)
	got := windowText(sh.settings.Window())
	if !strings.Contains(got, ui.ToggleCommand) || strings.Contains(got, "Desktop keys") {
		t.Errorf("General page without the portal:\n%s", got)
	}
	if err := sh.changeDesktopShortcuts(); err == nil {
		t.Error("changing the desktop's keys without a portal succeeded")
	}
}
