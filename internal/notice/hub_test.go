package notice

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/output"
)

// fakeClock fires its timers when the test advances it.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.stopped
		t.stopped = true
		return was
	}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []func()
	for _, t := range c.timers {
		if !t.stopped && !t.at.After(c.now) {
			t.stopped = true
			due = append(due, t.f)
		}
	}
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

func newTestHub(t *testing.T) (*Hub, *fakeClock) {
	t.Helper()
	c := newFakeClock()
	return NewHub(Options{Clock: c, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}), c
}

func next(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("subscription closed")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
		return Event{}
	}
}

func none(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}
}

func codes(ns []Notice) string {
	var s []string
	for _, n := range ns {
		s = append(s, string(n.Code))
	}
	return strings.Join(s, ",")
}

func TestPublishFansOutToEverySubscriber(t *testing.T) {
	h, c := newTestHub(t)
	a, stopA := h.Subscribe()
	defer stopA()
	b, stopB := h.Subscribe()
	defer stopB()

	if !h.Publish(NoModel()) {
		t.Fatal("the first notice was taken for a duplicate")
	}
	for _, ch := range []<-chan Event{a, b} {
		ev := next(t, ch)
		if ev.Ended || ev.Notice.Code != CodeNoModel || !ev.Notice.At.Equal(c.Now()) || codes(ev.Active) != "no_model" {
			t.Errorf("event %+v", ev)
		}
	}
	h.Publish(New(CodePasteFailed, "boom"))
	ev := next(t, a)
	if codes(ev.Active) != "paste_failed,no_model" {
		t.Errorf("active %s, want newest first", codes(ev.Active))
	}
	next(t, b)
}

func TestDuplicatesRaiseNothing(t *testing.T) {
	h, _ := newTestHub(t)
	ch, stop := h.Subscribe()
	defer stop()
	h.Publish(MicrophoneUnavailable("first"))
	next(t, ch)
	if h.Publish(MicrophoneUnavailable("second")) {
		t.Error("a duplicate was raised")
	}
	none(t, ch)
	if n := h.Count(CodeMicrophoneUnavailable); n != 2 {
		t.Errorf("count %d", n)
	}
	if got := h.Active()[0].Message; got != "second" {
		t.Errorf("message %q, want the latest", got)
	}
}

// Unplugging the microphone mid-recording makes the recorder report several
// things in a row (the device is gone, the fallback, a reopen); the user
// hears about it once.
func TestMicrophoneUnplugShowsOneNotice(t *testing.T) {
	h, c := newTestHub(t)
	ch, stop := h.Subscribe()
	defer stop()
	for _, msg := range []string{
		`The microphone "Yeti X" is not connected; using the default microphone (MacBook Pro Microphone).`,
		`The microphone "Yeti X" could not be opened (device lost); using the default microphone.`,
		"The microphone could not be reopened: no device",
	} {
		h.Publish(MicrophoneUnavailable(msg))
		c.Advance(time.Second)
	}
	if ev := next(t, ch); ev.Notice.Code != CodeMicrophoneUnavailable {
		t.Fatalf("event %+v", ev)
	}
	none(t, ch)
}

func TestTransientNoticesExpireAfterQuiet(t *testing.T) {
	h, c := newTestHub(t)
	ch, stop := h.Subscribe()
	defer stop()
	h.Publish(New(CodePasteFailed, "one"))
	next(t, ch)

	// Repeats keep it active: the quiet period starts again each time.
	c.Advance(DefaultQuiet - time.Second)
	h.Publish(New(CodePasteFailed, "two"))
	c.Advance(DefaultQuiet - time.Second)
	none(t, ch)
	if len(h.Active()) != 1 {
		t.Fatal("expired while repeating")
	}

	c.Advance(time.Second)
	if ev := next(t, ch); !ev.Ended || ev.Notice.Code != CodePasteFailed || len(ev.Active) != 0 {
		t.Fatalf("event %+v, want the paste notice to end", ev)
	}
	// Much later, the same failure is news again.
	c.Advance(time.Hour)
	if !h.Publish(New(CodePasteFailed, "three")) {
		t.Error("a new occurrence after the quiet period was taken for a duplicate")
	}
	next(t, ch)
}

func TestPersistentNoticesStayUntilResolved(t *testing.T) {
	h, c := newTestHub(t)
	ch, stop := h.Subscribe()
	defer stop()
	h.Publish(AccessibilityMissing())
	next(t, ch)
	c.Advance(24 * time.Hour)
	none(t, ch)
	if !h.Resolve(CodePermissionMissing) {
		t.Fatal("Resolve found nothing")
	}
	if ev := next(t, ch); !ev.Ended || ev.Notice.Code != CodePermissionMissing {
		t.Fatalf("event %+v", ev)
	}
	if h.Resolve(CodePermissionMissing) {
		t.Error("resolved twice")
	}
	if h.Count(CodePermissionMissing) != 0 {
		t.Error("count of an inactive notice")
	}
}

// A notice resolved and raised again is not ended by the first one's timer.
func TestStaleExpiryDoesNotEndANewNotice(t *testing.T) {
	h, c := newTestHub(t)
	ch, stop := h.Subscribe()
	defer stop()
	h.Publish(New(CodeEngineCrashed, "one"))
	next(t, ch)
	h.Resolve(CodeEngineCrashed)
	next(t, ch)
	c.Advance(DefaultQuiet / 2)
	h.Publish(New(CodeEngineCrashed, "two"))
	next(t, ch)
	c.Advance(DefaultQuiet / 2) // the first timer's deadline, had it not been stopped
	none(t, ch)
	if len(h.Active()) != 1 {
		t.Error("the new notice ended early")
	}
}

// A subscriber that does not read holds up neither the publisher nor the
// other subscribers, and later receives everything in order.
func TestSlowSubscriberBlocksNobody(t *testing.T) {
	h, _ := newTestHub(t)
	slow, stopSlow := h.Subscribe()
	defer stopSlow()
	fast, stopFast := h.Subscribe()
	defer stopFast()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 100 {
			h.Publish(New(Code(fmt.Sprintf("code_%d", i)), "x"))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a subscriber that does not read")
	}
	for i := range 100 {
		if ev := next(t, fast); ev.Notice.Code != Code(fmt.Sprintf("code_%d", i)) {
			t.Fatalf("fast subscriber: event %d is %s", i, ev.Notice.Code)
		}
	}
	for i := range 100 {
		if ev := next(t, slow); ev.Notice.Code != Code(fmt.Sprintf("code_%d", i)) {
			t.Fatalf("slow subscriber: event %d is %s", i, ev.Notice.Code)
		}
	}
}

func TestUnsubscribeClosesTheChannel(t *testing.T) {
	h, _ := newTestHub(t)
	ch, stop := h.Subscribe()
	h.Publish(NoModel()) // queued, never read
	stop()
	stop() // twice is fine
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				h.Publish(New(CodePasteFailed, "after")) // no panic
				return
			}
		case <-deadline:
			t.Fatal("the channel did not close")
		}
	}
}

func TestNoticeKinds(t *testing.T) {
	for _, code := range []Code{
		CodeNoModel, CodeMicrophoneUnavailable, CodePermissionMissing, CodeModelCorrupt,
		CodeEngineCrashed, CodeEngineCPUFallback, CodePasteFailed, CodeShortcutFailed,
	} {
		n := New(code, "m")
		if n.Title() == string(code) || n.Source == "" {
			t.Errorf("%s has no title or source: %+v", code, n)
		}
	}
	if !NoModel().Persistent() || !AccessibilityMissing().Persistent() || New(CodePasteFailed, "").Persistent() {
		t.Error("persistence: only a missing model or permission persists")
	}
	if New(CodeEngineCPUFallback, "").Level != LevelWarning || New(CodeEngineCrashed, "").Level != LevelError {
		t.Error("levels")
	}
	if a := NoModel().Action; a.ID != ActionOpenModels || a.Label != "Download a model" {
		t.Errorf("no-model action %+v", a)
	}
	if a := ModelCorrupt("Parakeet TDT 0.6B v2").Action; a.ID != ActionOpenModels {
		t.Errorf("corrupt-model action %+v", a)
	}
	if New("unknown", "").Title() != "unknown" || LevelError.String() != "error" || LevelWarning.String() != "warning" {
		t.Error("fallbacks")
	}
}

func TestForPasteError(t *testing.T) {
	denied := &output.Error{Op: "paste", Err: fmt.Errorf("%w: grant it", output.ErrNoPermission)}
	if n := ForPasteError(denied); n.Code != CodePermissionMissing || n.Action.ID != ActionOpenAccessibility {
		t.Errorf("permission refusal: %+v", n)
	}
	n := ForPasteError(&output.Error{Op: "copy", Err: errors.New("pasteboard busy")})
	if n.Code != CodePasteFailed || n.Message != "The transcript could not be pasted: the copy step failed (pasteboard busy). It may still be on the clipboard." {
		t.Errorf("other failure: %+v", n)
	}
}

func TestForShortcutError(t *testing.T) {
	err := errors.New(`the transcribe shortcut "option+space" is not available: taken`)
	if n := ForShortcutError(err, false); n.Code != CodePermissionMissing {
		t.Errorf("untrusted: %+v", n)
	}
	n := ForShortcutError(err, true)
	if n.Code != CodeShortcutFailed || !strings.HasPrefix(n.Message, `The transcribe shortcut "option+space" is not available: taken.`) || n.Action.ID != ActionOpenSettings {
		t.Errorf("trusted: %+v", n)
	}
}
