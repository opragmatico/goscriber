package portal

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/hotkey"
)

func TestTrigger(t *testing.T) {
	for _, tc := range []struct {
		purpose hotkey.Purpose
		in      string
		want    string
	}{
		{hotkey.PurposeTranscribe, "ctrl+alt+space", "CTRL+ALT+space"},
		{hotkey.PurposeTranscribe, "Shift+Super+D", "SHIFT+LOGO+d"},
		{hotkey.PurposeTranscribe, "option+cmd+return", "ALT+LOGO+Return"},
		{hotkey.PurposeTranscribe, "f5", "F5"},
		{hotkey.PurposeTranscribe, "ctrl+f12", "CTRL+F12"},
		{hotkey.PurposeTranscribe, "alt+7", "ALT+7"},
		{hotkey.PurposeTranscribe, "ctrl+up", "CTRL+Up"},
		{hotkey.PurposePostProcess, "ctrl+alt+shift+space", "CTRL+ALT+SHIFT+space"},
		{hotkey.PurposeCancel, "ctrl+escape", "CTRL+Escape"},
		{hotkey.PurposeCancel, "alt+tab", "ALT+Tab"},
		// Plain keys the desktop would take from every app.
		{hotkey.PurposeCancel, "escape", ""},
		{hotkey.PurposeTranscribe, "space", ""},
		{hotkey.PurposePostProcess, "shift+escape", "SHIFT+Escape"},
		{hotkey.PurposePostProcess, "escape", ""},
		{hotkey.PurposeTranscribe, "", ""},
		{hotkey.PurposeTranscribe, "ctrl+nope", ""},
	} {
		if got := Trigger(tc.purpose, tc.in); got != tc.want {
			t.Errorf("Trigger(%s, %q) = %q, want %q", tc.purpose, tc.in, got, tc.want)
		}
	}
}

func TestBindsTheShortcutsAndDeliversTheirKeys(t *testing.T) {
	addr := newBus(t)
	f := newFakePortal(t, addr, 1)
	suggest := map[hotkey.Purpose]string{
		hotkey.PurposeTranscribe: "ctrl+alt+space", hotkey.PurposePostProcess: "ctrl+alt+shift+space", hotkey.PurposeCancel: "escape",
	}
	b, statuses := openTest(t, addr, Options{Suggest: func(p hotkey.Purpose) string { return suggest[p] }})

	if s := <-statuses; s.State != Asking || s.Configurable {
		t.Fatalf("first status = %+v, want Asking and not configurable", s)
	}
	st := waitState(t, statuses, Bound)
	want := map[hotkey.Purpose]string{
		hotkey.PurposeTranscribe: "CTRL+ALT+space", hotkey.PurposePostProcess: "CTRL+ALT+SHIFT+space", hotkey.PurposeCancel: "",
	}
	if !maps.Equal(st.Triggers, want) {
		t.Fatalf("triggers = %v, want %v", st.Triggers, want)
	}
	f.mu.Lock()
	appID := f.appID
	f.mu.Unlock()
	if appID != AppID {
		t.Errorf("registered app ID %q, want %q", appID, AppID)
	}

	// The desktop is asked for the three shortcuts, with the transcribe
	// and post-processing ones suggested and the cancel one's plain Escape
	// not.
	asked := f.askedFor()
	if len(asked) != 3 || asked[0].ID != "transcribe" || asked[1].ID != "post_process" || asked[2].ID != "cancel" {
		t.Fatalf("asked for %+v", asked)
	}
	if got, _ := asked[0].Props["preferred_trigger"].Value().(string); got != "CTRL+ALT+space" {
		t.Errorf("transcribe preferred_trigger = %q", got)
	}
	if got, _ := asked[1].Props["preferred_trigger"].Value().(string); got != "CTRL+ALT+SHIFT+space" {
		t.Errorf("post_process preferred_trigger = %q", got)
	}
	if _, ok := asked[2].Props["preferred_trigger"]; ok {
		t.Errorf("cancel has a preferred_trigger: %v", asked[2].Props)
	}
	if d, _ := asked[0].Props["description"].Value().(string); d != TranscribeDescription {
		t.Errorf("transcribe description = %q", d)
	}
	if d, _ := asked[1].Props["description"].Value().(string); d != PostProcessDescription {
		t.Errorf("post_process description = %q", d)
	}

	transcribe, err := b.Register(hotkey.PurposeTranscribe, hotkey.Binding{})
	if err != nil {
		t.Fatal(err)
	}
	cancel, err := b.Register(hotkey.PurposeCancel, hotkey.Binding{})
	if err != nil {
		t.Fatal(err)
	}
	session := f.currentSession()
	f.press(session, "transcribe", true)
	f.press(session, "transcribe", true) // a repeat is not a second press
	f.press("/org/freedesktop/portal/desktop/session/other/x", "transcribe", false)
	f.press(session, "cancel", true)
	f.press(session, "transcribe", false)
	expect(t, transcribe.Events(), hotkey.KeyDown, hotkey.KeyUp)
	expect(t, cancel.Events(), hotkey.KeyDown)

	// Unregistering stops the events; registering again starts them.
	if err := transcribe.Unregister(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-transcribe.Events(); ok {
		t.Fatal("events still open after Unregister")
	}
	f.press(session, "transcribe", true)
	waitFor(t, func() bool { return b.isDown("transcribe") })
	f.press(session, "transcribe", false)
	waitFor(t, func() bool { return !b.isDown("transcribe") })
	again, err := b.Register(hotkey.PurposeTranscribe, hotkey.Binding{})
	if err != nil {
		t.Fatal(err)
	}
	f.press(session, "transcribe", true)
	expect(t, again.Events(), hotkey.KeyDown)

	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if got := f.closedSessions(); len(got) != 1 || got[0] != session {
		t.Errorf("closed sessions %v, want %s", got, session)
	}
	if _, err := b.Register(hotkey.PurposeTranscribe, hotkey.Binding{}); !errors.Is(err, ErrClosed) {
		t.Errorf("Register after Close: %v", err)
	}
}

// expect reads want from events and checks nothing else arrives.
func expect(t *testing.T, events <-chan hotkey.KeyEvent, want ...hotkey.KeyEvent) {
	t.Helper()
	for i, w := range want {
		select {
		case ev := <-events:
			if ev != w {
				t.Fatalf("event %d = %v, want %v", i, ev, w)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no event %d (%v)", i, w)
		}
	}
	select {
	case ev := <-events:
		t.Fatalf("unexpected event %v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPushToTalkThroughTheManager(t *testing.T) {
	addr := newBus(t)
	f := newFakePortal(t, addr, 1)
	b, statuses := openTest(t, addr, Options{})
	waitState(t, statuses, Bound)

	actions := make(chan hotkey.Action, 8)
	registered := make(chan hotkey.Purpose, 4)
	m := hotkey.NewManager(b, hotkey.Options{
		OnAction:     func(a hotkey.Action) { actions <- a },
		OnRegistered: func(p hotkey.Purpose) { registered <- p },
	})
	defer func() {
		m.Close()
		<-m.Done()
	}()
	m.Apply(hotkey.Config{Transcribe: "ctrl+alt+space", Cancel: "escape", Mode: config.ActivationPushToTalk})
	if p := <-registered; p != hotkey.PurposeTranscribe {
		t.Fatalf("registered %v first", p)
	}

	session := f.currentSession()
	f.press(session, "transcribe", true)
	if a := <-actions; a != hotkey.ActionStart {
		t.Fatalf("action %v, want start", a)
	}
	m.SetRecording(true)
	f.press(session, "transcribe", false)
	if a := <-actions; a != hotkey.ActionStop {
		t.Fatalf("action %v, want stop", a)
	}
}

func TestRefusedBinding(t *testing.T) {
	addr := newBus(t)
	newFakePortal(t, addr, 1, func(f *fakePortal) {
		f.answer = func([]shortcut) (uint32, []shortcut) { return 1, nil }
	})
	b, statuses := openTest(t, addr, Options{})

	st := waitState(t, statuses, Refused)
	if !errors.Is(st.Err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", st.Err)
	}
	if _, err := b.Register(hotkey.PurposeTranscribe, hotkey.Binding{}); !errors.Is(err, ErrRefused) {
		t.Errorf("Register: %v, want ErrRefused", err)
	}
}

func TestShortcutLeftOut(t *testing.T) {
	addr := newBus(t)
	newFakePortal(t, addr, 1, func(f *fakePortal) {
		f.answer = func([]shortcut) (uint32, []shortcut) {
			return 0, []shortcut{described("transcribe", "Ctrl+Alt+Space")}
		}
	})
	b, statuses := openTest(t, addr, Options{})
	st := waitState(t, statuses, Bound)
	if _, ok := st.Triggers[hotkey.PurposeCancel]; ok {
		t.Errorf("triggers %v include cancel", st.Triggers)
	}
	if _, err := b.Register(hotkey.PurposeTranscribe, hotkey.Binding{}); err != nil {
		t.Errorf("Register transcribe: %v", err)
	}
	if _, err := b.Register(hotkey.PurposeCancel, hotkey.Binding{}); !errors.Is(err, ErrNotBound) {
		t.Errorf("Register cancel: %v, want ErrNotBound", err)
	}
}

func TestRegisterWhileTheDesktopAsks(t *testing.T) {
	addr := newBus(t)
	hold := make(chan struct{})
	f := newFakePortal(t, addr, 1, func(f *fakePortal) { f.hold = hold })
	b, statuses := openTest(t, addr, Options{})
	waitFor(t, func() bool { return len(f.askedFor()) > 0 })

	r, err := b.Register(hotkey.PurposeTranscribe, hotkey.Binding{})
	if err != nil {
		t.Fatalf("Register while asking: %v", err)
	}
	close(hold)
	waitState(t, statuses, Bound)
	f.press(f.currentSession(), "transcribe", true)
	expect(t, r.Events(), hotkey.KeyDown)
}

func TestCloseWhileTheDesktopAsks(t *testing.T) {
	addr := newBus(t)
	hold := make(chan struct{})
	defer close(hold)
	f := newFakePortal(t, addr, 1, func(f *fakePortal) { f.hold = hold })
	b, statuses := openTest(t, addr, Options{})
	waitFor(t, func() bool { return len(f.askedFor()) > 0 })

	done := make(chan struct{})
	go func() {
		_ = b.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close waited for the desktop's answer")
	}
	for {
		select {
		case s := <-statuses:
			if s.State != Asking {
				t.Fatalf("status after Close: %+v", s)
			}
			continue
		default:
		}
		break
	}
	if got := f.closedSessions(); len(got) != 1 {
		t.Errorf("closed sessions %v, want the one asking", got)
	}
}

func TestShortcutsChanged(t *testing.T) {
	addr := newBus(t)
	f := newFakePortal(t, addr, 1)
	_, statuses := openTest(t, addr, Options{Suggest: func(p hotkey.Purpose) string {
		return map[hotkey.Purpose]string{hotkey.PurposeTranscribe: "ctrl+alt+space", hotkey.PurposeCancel: "ctrl+escape"}[p]
	}})
	waitState(t, statuses, Bound)

	f.emit("ShortcutsChanged", dbus.ObjectPath("/org/freedesktop/portal/desktop/session/other/x"),
		[]shortcut{described("transcribe", "Nope")})
	f.emit("ShortcutsChanged", f.currentSession(),
		[]shortcut{described("transcribe", "Super+D"), described("cancel", "Ctrl+Escape")})
	select {
	case s := <-statuses:
		if s.State != Bound || s.Triggers[hotkey.PurposeTranscribe] != "Super+D" || s.Triggers[hotkey.PurposeCancel] != "Ctrl+Escape" {
			t.Fatalf("status after ShortcutsChanged: %+v", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no status after ShortcutsChanged")
	}
}

func TestConfigure(t *testing.T) {
	addr := newBus(t)
	f := newFakePortal(t, addr, 2)
	b, statuses := openTest(t, addr, Options{})
	if st := waitState(t, statuses, Bound); !st.Configurable {
		t.Error("version 2 is not configurable")
	}
	if err := b.Configure(); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	configured := f.configured
	f.mu.Unlock()
	if len(configured) != 1 || configured[0] != f.currentSession() {
		t.Errorf("configured %v, want the session", configured)
	}

	addr1 := newBus(t)
	newFakePortal(t, addr1, 1)
	b1, statuses1 := openTest(t, addr1, Options{})
	waitState(t, statuses1, Bound)
	if err := b1.Configure(); err == nil {
		t.Error("Configure on version 1 succeeded")
	}
}

func TestOldPortalRequestHandles(t *testing.T) {
	addr := newBus(t)
	newFakePortal(t, addr, 1, func(f *fakePortal) { f.ownHandles = true })
	_, statuses := openTest(t, addr, Options{})
	waitState(t, statuses, Bound)
}

func TestUnavailable(t *testing.T) {
	addr := newBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// No portal on the bus at all.
	if _, err := Open(ctx, Options{Conn: connect(t, addr)}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("no portal: %v, want ErrUnavailable", err)
	}
	// A portal without GlobalShortcuts.
	newFakePortal(t, addr, 0)
	if _, err := Open(ctx, Options{Conn: connect(t, addr)}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("no GlobalShortcuts: %v, want ErrUnavailable", err)
	}
}

// isDown is whether the Backend has heard the shortcut id go down last.
func (b *Backend) isDown(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.down[id]
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
