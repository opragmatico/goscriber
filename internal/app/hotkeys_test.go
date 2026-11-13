package app

import (
	"bytes"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/hotkey"
)

// testShortcuts stands in for the OS's global shortcuts in every app test,
// so no test registers a real one.
var testShortcuts = &fakeShortcuts{}

func init() {
	newHotkeyBackend = func() hotkey.Backend { return testShortcuts }
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

func (f *fakeShortcuts) Register(b hotkey.Binding) (hotkey.Registration, error) {
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

func TestShortcutDrivesDictationAndFollowsSettings(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m", "transcribe_shortcut": "ctrl+shift+d", "activation_mode": "toggle"}`)
	var stderr syncBuffer
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{Stderr: &stderr})
	testShortcuts.waitLog(t, "+ctrl+shift+d")

	// The press reaches dictation on the main thread. The stand-in never
	// records, so the shortcut stays idle and Escape is never taken.
	testShortcuts.press(t, "ctrl+shift+d")
	q.runNext(t)
	if !strings.Contains(stderr.String(), `dictation is not implemented yet" action=start`) {
		t.Errorf("stderr = %q", stderr.String())
	}
	testShortcuts.press(t, "ctrl+shift+d")
	q.runNext(t)
	if n := strings.Count(stderr.String(), "action=start"); n != 2 {
		t.Errorf("%d starts; with nothing recording, every toggle press starts", n)
	}

	// A new shortcut in the settings replaces the old one without a restart.
	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.TranscribeShortcut = "f5" }); err != nil {
		t.Fatal(err)
	}
	q.runNext(t)
	testShortcuts.waitLog(t, "+ctrl+shift+d", "-ctrl+shift+d", "+f5")

	// Quitting gives the shortcut back.
	sh.stopFollowing()
	testShortcuts.waitLog(t, "+ctrl+shift+d", "-ctrl+shift+d", "+f5", "-f5")
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
