package app

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/history"
	"github.com/opragmatico/goscriber/internal/hotkey"
	"github.com/opragmatico/goscriber/internal/ui"
)

// testShortcuts stands in for the OS's global shortcuts in every app test,
// so no test registers a real one.
var testShortcuts = &fakeShortcuts{}

func init() {
	newHotkeyBackend = func() hotkey.Backend { return testShortcuts }
	// No test asks a real desktop portal for shortcuts; shortcuts_test.go
	// puts a fake one in.
	openDesktopShortcuts = nil
}

type fakeShortcuts struct {
	mu     sync.Mutex
	active map[string]chan hotkey.KeyEvent
	log    []string
	fail   error // Register fails with it while set
}

func (f *fakeShortcuts) setFail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
}

type fakeShortcut struct {
	f   *fakeShortcuts
	key string
	ch  chan hotkey.KeyEvent
}

func (f *fakeShortcuts) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active, f.log, f.fail = map[string]chan hotkey.KeyEvent{}, nil, nil
}

func (f *fakeShortcuts) Register(_ hotkey.Purpose, b hotkey.Binding) (hotkey.Registration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		f.log = append(f.log, "!"+b.String())
		return nil, f.fail
	}
	if f.active == nil {
		f.active = map[string]chan hotkey.KeyEvent{}
	}
	ch := make(chan hotkey.KeyEvent, 4)
	f.active[b.String()] = ch
	f.log = append(f.log, "+"+b.String())
	return &fakeShortcut{f: f, key: b.String(), ch: ch}, nil
}

func (r *fakeShortcut) Events() <-chan hotkey.KeyEvent { return r.ch }

func (r *fakeShortcut) Unregister() error {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	delete(r.f.active, r.key)
	r.f.log = append(r.f.log, "-"+r.key)
	close(r.ch)
	return nil
}

// waitLog waits until the registrations are want.
func (f *fakeShortcuts) waitLog(t *testing.T, want ...string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		got := slices.Clone(f.log)
		f.mu.Unlock()
		if slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("registrations %v, want %v", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *fakeShortcuts) press(t *testing.T, key string) {
	t.Helper()
	f.mu.Lock()
	ch := f.active[key]
	f.mu.Unlock()
	if ch == nil {
		t.Fatalf("%s is not registered", key)
	}
	ch <- hotkey.KeyDown
	ch <- hotkey.KeyUp
}

// down sends only a key-down, for a shortcut that may be unregistered as
// soon as it is heard, such as Escape.
func (f *fakeShortcuts) down(t *testing.T, key string) {
	t.Helper()
	f.mu.Lock()
	ch := f.active[key]
	f.mu.Unlock()
	if ch == nil {
		t.Fatalf("%s is not registered", key)
	}
	ch <- hotkey.KeyDown
}

func TestShortcutFollowsSettings(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m", "transcribe_shortcut": "ctrl+shift+d", "activation_mode": "toggle"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	polish := "+" + defaultPolish(t)
	testShortcuts.waitLog(t, "+ctrl+shift+d", polish)

	// A new shortcut in the settings replaces the old one without a restart,
	// and leaves the post-processing shortcut as it is.
	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.TranscribeShortcut = "f5" }); err != nil {
		t.Fatal(err)
	}
	q.runNext(t)
	testShortcuts.waitLog(t, "+ctrl+shift+d", polish, "-ctrl+shift+d", "+f5")

	// So does a new post-processing shortcut.
	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.PostProcessShortcut = "shift+f5" }); err != nil {
		t.Fatal(err)
	}
	q.runNext(t)
	testShortcuts.waitLog(t, "+ctrl+shift+d", polish, "-ctrl+shift+d", "+f5", "-"+defaultPolish(t), "+shift+f5")

	// Quitting gives the shortcuts back.
	sh.stopFollowing()
	testShortcuts.waitLog(t, "+ctrl+shift+d", polish, "-ctrl+shift+d", "+f5", "-"+defaultPolish(t), "+shift+f5", "-f5", "-shift+f5")
}

// The post-processing shortcut dictates and post-processes the transcript
// with llm_enabled off; the dictation shortcut, beside it, does not.
func TestPostProcessShortcutDictates(t *testing.T) {
	srv, _ := fakeLLM(t, 0)
	home := isolate(t)
	testShortcuts.reset()
	writeSettings(t, home, `{"schema_version": 2, "selected_model": "m", "activation_mode": "toggle",
		"transcribe_shortcut": "ctrl+shift+d", "post_process_shortcut": "ctrl+shift+p",
		"llm_enabled": false, "llm_base_url": "`+srv.URL+`/v1", "llm_model": "test", "llm_prompt": "[${output}]"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	testShortcuts.waitLog(t, "+ctrl+shift+d", "+ctrl+shift+p")
	out := sh.svc.output.(*testOutput)

	dictate := func(key string, n int) {
		t.Helper()
		testShortcuts.press(t, key)
		q.waitUI(t, "recording", func() bool { return sh.recording })
		testShortcuts.press(t, key)
		q.waitUI(t, "the transcript", func() bool { return len(out.delivered()) == n })
		q.waitUI(t, "idle", func() bool { return a.trayIcon == ui.TrayIcon(ui.TrayIdle) })
	}
	dictate("ctrl+shift+p", 1)
	dictate("ctrl+shift+d", 2)
	if got := out.delivered(); len(got) != 2 || got[0] != "HELLO FROM THE TEST" || strings.ToUpper(got[1]) == got[1] {
		t.Errorf("delivered %q, want the first dictation post-processed and the second not", got)
	}
	// The history keeps the model's text for the first only.
	var entries []history.Entry
	for deadline := time.Now().Add(2 * time.Second); len(entries) < 2 && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		entries, _ = sh.history.List(context.Background(), 0)
	}
	if len(entries) != 2 || entries[1].ProcessedText != "HELLO FROM THE TEST" || entries[0].ProcessedText != "" {
		t.Errorf("history %+v, want the first dictation's processed text kept", entries)
	}
}

// While the settings window records a shortcut, the global shortcuts are
// given back to the OS, so the keys pressed reach the window; the new one
// registers once it is recorded.
func TestSettingsWindowSuspendsShortcutsWhileRecording(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	writeSettings(t, home, `{"schema_version": 1, "transcribe_shortcut": "ctrl+shift+d"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	polish := "+" + defaultPolish(t)
	testShortcuts.waitLog(t, "+ctrl+shift+d", polish)

	sh.settings.Show(ui.PageGeneral)
	var in *ui.ShortcutInput
	for _, o := range test.LaidOutObjects(sh.settings.Window().Content()) {
		if s, ok := o.(*ui.ShortcutInput); ok && in == nil {
			in = s // the dictation shortcut comes first
		}
	}
	if in == nil {
		t.Fatal("no shortcut field on the General page")
	}
	test.Tap(in)
	testShortcuts.waitLog(t, "+ctrl+shift+d", polish, "-ctrl+shift+d", "-"+defaultPolish(t))
	for _, k := range []fyne.KeyName{"LeftControl", "LeftShift", fyne.KeyK} {
		in.KeyDown(&fyne.KeyEvent{Name: k})
	}
	if got := sh.rt.Settings.Get().TranscribeShortcut; got != "ctrl+shift+k" {
		t.Fatalf("saved %q", got)
	}
	q.runNext(t) // the shell applies the new settings
	testShortcuts.waitLog(t, "+ctrl+shift+d", polish, "-ctrl+shift+d", "-"+defaultPolish(t),
		"+ctrl+shift+d", polish, "-ctrl+shift+d", "+ctrl+shift+k")
}

// syncBuffer is a bytes.Buffer the logger and the test can share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestSecureInputWarning(t *testing.T) {
	isolate(t)
	testShortcuts.reset()
	var stderr syncBuffer
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, _ := newTestShell(t, a, Options{Stderr: &stderr})
	var mu sync.Mutex
	on := true
	sh.secureInput = func() bool { mu.Lock(); defer mu.Unlock(); return on }
	poll, sustain := secureInputPoll, secureInputSustain
	secureInputPoll, secureInputSustain = 5*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { secureInputPoll, secureInputSustain = poll, sustain })
	sh.started()

	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !strings.Contains(stderr.String(), want) {
			if time.Now().After(deadline) {
				t.Fatalf("%q not logged: %q", want, stderr.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor("Secure Event Input is on, so the shortcut cannot be heard")
	mu.Lock()
	on = false
	mu.Unlock()
	waitFor("Secure Event Input is off; the shortcut works again")
}
