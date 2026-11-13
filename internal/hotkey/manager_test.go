package hotkey

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
)

// fakeBackend records registrations and lets the test press their keys.
type fakeBackend struct {
	mu     sync.Mutex
	active map[string]*fakeReg // by Binding.String()
	log    []string            // "+alt+space", "-escape", "!f5"
	fail   map[string]error    // Register fails for these shortcuts
}

type fakeReg struct {
	be     *fakeBackend
	key    string
	events chan KeyEvent
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{active: map[string]*fakeReg{}, fail: map[string]error{}}
}

func (f *fakeBackend) Register(b Binding) (Registration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := b.String()
	if err := f.fail[key]; err != nil {
		f.log = append(f.log, "!"+key)
		return nil, err
	}
	if f.active[key] != nil {
		return nil, errors.New("already registered")
	}
	r := &fakeReg{be: f, key: key, events: make(chan KeyEvent, 8)}
	f.active[key] = r
	f.log = append(f.log, "+"+key)
	return r, nil
}

func (r *fakeReg) Events() <-chan KeyEvent { return r.events }

func (r *fakeReg) Unregister() error {
	r.be.mu.Lock()
	defer r.be.mu.Unlock()
	if r.be.active[r.key] != r {
		return errors.New("not registered")
	}
	delete(r.be.active, r.key)
	r.be.log = append(r.be.log, "-"+r.key)
	close(r.events)
	return nil
}

func (f *fakeBackend) setFail(key string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[key] = err
}

// send delivers a key event to the registered shortcut key, failing the
// test when it is not registered.
func (f *fakeBackend) send(t *testing.T, key string, ev KeyEvent) {
	t.Helper()
	f.mu.Lock()
	r := f.active[key]
	f.mu.Unlock()
	if r == nil {
		t.Fatalf("%s is not registered (log %v)", key, f.logged())
	}
	r.events <- ev
}

func (f *fakeBackend) logged() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.log)
}

func (f *fakeBackend) isActive(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[key] != nil
}

// harness runs a Manager on a fake backend and clock, collecting actions and
// errors.
type harness struct {
	t       *testing.T
	be      *fakeBackend
	m       *Manager
	actions chan Action
	errs    chan error

	mu   sync.Mutex
	now  time.Time
	regs []Purpose
}

func (h *harness) registered() []Purpose {
	h.sync()
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.regs)
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t: t, be: newFakeBackend(),
		actions: make(chan Action, 16), errs: make(chan error, 16),
		now: time.Unix(1000, 0),
	}
	h.m = newManager(h.be, Options{
		OnAction:     func(a Action) { h.actions <- a },
		OnError:      func(err error) { h.errs <- err },
		OnRegistered: func(p Purpose) { h.mu.Lock(); h.regs = append(h.regs, p); h.mu.Unlock() },
	}, h.clock)
	t.Cleanup(func() {
		h.m.Close()
		<-h.m.Done()
	})
	return h
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

// sync waits until the Manager has handled everything queued so far.
func (h *harness) sync() {
	h.t.Helper()
	done := make(chan struct{})
	h.m.post(func() { close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		h.t.Fatal("the manager is stuck")
	}
}

func (h *harness) wantAction(want Action) {
	h.t.Helper()
	select {
	case got := <-h.actions:
		if got != want {
			h.t.Fatalf("action %s, want %s", got, want)
		}
	case <-time.After(2 * time.Second):
		h.t.Fatalf("no action, want %s", want)
	}
}

func (h *harness) wantNoAction() {
	h.t.Helper()
	h.sync()
	select {
	case got := <-h.actions:
		h.t.Fatalf("unexpected action %s", got)
	default:
	}
}

func (h *harness) wantError(purpose Purpose) *RegisterError {
	h.t.Helper()
	select {
	case err := <-h.errs:
		var rerr *RegisterError
		if !errors.As(err, &rerr) || rerr.Purpose != purpose {
			h.t.Fatalf("error %v, want a %s RegisterError", err, purpose)
		}
		return rerr
	case <-time.After(2 * time.Second):
		h.t.Fatalf("no error, want a %s RegisterError", purpose)
		return nil
	}
}

func (h *harness) wantLog(want ...string) {
	h.t.Helper()
	h.sync()
	if got := h.be.logged(); !slices.Equal(got, want) {
		h.t.Fatalf("registrations %v, want %v", got, want)
	}
}

// talk is how the backend names cfg's transcribe shortcut, "option+space":
// Binding.String uses this OS's modifier names, so it is "option+space" on
// macOS and "alt+space" elsewhere. Settings keep the text as typed, so
// errors quote "option+space" on every OS.
var talk = mustParse("option+space").String()

func mustParse(s string) Binding {
	b, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return b
}

func cfg(mode config.ActivationMode) Config {
	return Config{Transcribe: "option+space", Cancel: "escape", Mode: mode, HoldThreshold: 300 * time.Millisecond}
}

func TestManagerRegistersOnApply(t *testing.T) {
	h := newHarness(t)
	h.wantLog() // nothing before Apply
	h.m.Apply(cfg(config.ActivationToggle))
	h.wantLog("+" + talk) // the cancel shortcut waits for a recording
	h.m.Apply(cfg(config.ActivationPushToTalk))
	h.wantLog("+" + talk) // only the mode changed
}

func TestManagerPushToTalkWithCancelWhileRecording(t *testing.T) {
	h := newHarness(t)
	h.m.Apply(cfg(config.ActivationPushToTalk))
	h.sync()

	h.be.send(t, talk, KeyDown)
	h.wantAction(ActionStart)
	h.wantLog("+" + talk)  // not confirmed yet: Escape stays free
	h.m.SetRecording(true) // dictation started
	h.wantLog("+"+talk, "+escape")

	h.advance(2 * time.Second)
	h.be.send(t, talk, KeyUp)
	h.wantAction(ActionStop)
	h.wantLog("+"+talk, "+escape", "-escape") // freed without waiting for dictation
	h.m.SetRecording(false)
	h.wantLog("+"+talk, "+escape", "-escape")

	// Cancel mid-recording; the release that follows does nothing.
	h.be.send(t, talk, KeyDown)
	h.wantAction(ActionStart)
	h.m.SetRecording(true)
	h.sync()
	h.be.send(t, "escape", KeyDown)
	h.wantAction(ActionCancel)
	h.be.send(t, talk, KeyUp)
	h.wantNoAction()
	if h.be.isActive("escape") {
		t.Error("escape is still registered after the cancel")
	}
}

func TestManagerHoldOrToggleUsesTheClock(t *testing.T) {
	h := newHarness(t)
	h.m.Apply(cfg(config.ActivationHoldOrToggle))
	h.sync()

	h.be.send(t, talk, KeyDown)
	h.wantAction(ActionStart)
	h.advance(100 * time.Millisecond) // a tap
	h.be.send(t, talk, KeyUp)
	h.wantNoAction()
	h.advance(3 * time.Second)
	h.be.send(t, talk, KeyDown)
	h.wantAction(ActionStop)
	h.be.send(t, talk, KeyUp)
	h.wantNoAction()

	h.be.send(t, talk, KeyDown)
	h.wantAction(ActionStart)
	h.advance(time.Second) // held
	h.be.send(t, talk, KeyUp)
	h.wantAction(ActionStop)
}

// A refusal to start (no model downloaded, say) leaves the shortcut idle, so
// the next press starts again and Escape is never taken.
func TestManagerFollowsARefusal(t *testing.T) {
	h := newHarness(t)
	h.m.Apply(cfg(config.ActivationToggle))
	h.sync()
	for range 2 {
		h.be.send(t, talk, KeyDown)
		h.wantAction(ActionStart)
		h.m.SetRecording(false)
		h.be.send(t, talk, KeyUp)
		h.wantNoAction()
	}
	h.wantLog("+" + talk)
}

func TestManagerReregistersOnSettingsChange(t *testing.T) {
	h := newHarness(t)
	h.m.Apply(cfg(config.ActivationToggle))
	h.m.SetRecording(true)
	h.wantLog("+"+talk, "+escape")

	next := cfg(config.ActivationToggle)
	next.Transcribe = "ctrl+shift+d"
	h.m.Apply(next)
	h.wantLog("+"+talk, "+escape", "-"+talk, "+ctrl+shift+d", "-escape", "+escape")

	next.Cancel = "shift+escape"
	h.m.Apply(next)
	h.wantLog("+"+talk, "+escape", "-"+talk, "+ctrl+shift+d", "-escape", "+escape", "-escape", "+shift+escape")

	// The new shortcut drives dictation; the old one is gone.
	h.m.SetRecording(false)
	h.sync()
	h.be.send(t, "ctrl+shift+d", KeyDown)
	h.wantAction(ActionStart)
	if h.be.isActive(talk) {
		t.Error("the old shortcut is still registered")
	}
}

func TestManagerReportsFailuresAndRetries(t *testing.T) {
	h := newHarness(t)
	denied := errors.New("grant Accessibility")
	h.be.setFail(talk, denied)
	h.m.Apply(cfg(config.ActivationToggle))
	rerr := h.wantError(PurposeTranscribe)
	if rerr.Shortcut != "option+space" || !errors.Is(rerr, denied) {
		t.Errorf("error %+v", rerr)
	}
	if msg := rerr.Error(); msg != `the transcribe shortcut "option+space" is not available: grant Accessibility` {
		t.Errorf("message %q", msg)
	}

	// Applying the same settings again, once the permission is granted,
	// retries the failed shortcut.
	h.be.setFail(talk, nil)
	h.m.Apply(cfg(config.ActivationToggle))
	h.wantLog("!"+talk, "+"+talk)
	if got := h.registered(); !slices.Equal(got, []Purpose{PurposeTranscribe}) {
		t.Errorf("OnRegistered saw %v, want only the successful registration", got)
	}
}

func TestManagerRejectsInvalidShortcuts(t *testing.T) {
	h := newHarness(t)
	bad := cfg(config.ActivationToggle)
	bad.Transcribe = "space"
	h.m.Apply(bad)
	if rerr := h.wantError(PurposeTranscribe); rerr.Shortcut != "space" {
		t.Errorf("error %+v", rerr)
	}

	same := cfg(config.ActivationToggle)
	same.Cancel = same.Transcribe
	h.m.Apply(same)
	h.m.SetRecording(true)
	if rerr := h.wantError(PurposeCancel); rerr.Error() != `the cancel shortcut "option+space" is not available: it is the transcribe shortcut too` {
		t.Errorf("error %v", rerr)
	}
	h.wantLog("+" + talk)
}

func TestManagerCloseUnregistersEverything(t *testing.T) {
	h := newHarness(t)
	h.m.Apply(cfg(config.ActivationToggle))
	h.m.SetRecording(true)
	h.sync()
	h.m.Close()
	select {
	case <-h.m.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not finish")
	}
	if got := h.be.logged(); !slices.Equal(got, []string{"+" + talk, "+escape", "-" + talk, "-escape"}) {
		t.Errorf("registrations %v", got)
	}
	h.m.Apply(cfg(config.ActivationToggle)) // after Close: ignored, no panic
}

func TestConfigFrom(t *testing.T) {
	s := config.Defaults()
	s.TranscribeShortcut, s.CancelShortcut = "f5", "shift+escape"
	s.ActivationMode, s.HoldThresholdMS = config.ActivationPushToTalk, 450
	want := Config{Transcribe: "f5", Cancel: "shift+escape", Mode: config.ActivationPushToTalk, HoldThreshold: 450 * time.Millisecond}
	if got := ConfigFrom(s); got != want {
		t.Errorf("ConfigFrom = %+v, want %+v", got, want)
	}
}
