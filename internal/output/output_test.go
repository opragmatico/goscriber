package output

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
)

// world is a fake clipboard and keyboard sharing one timeline, so a test can
// check the order of everything Deliver did.
type world struct {
	mu      sync.Mutex
	text    string // the clipboard's contents
	marked  bool   // last SetText was transient
	changes int
	events  []string

	saveErr, setErr, pasteErr, typeErr, returnErr, restoreErr error

	onPaste func() // runs during Paste, as the target app would
	typed   string
}

func (w *world) logf(s string) { w.events = append(w.events, s) }

func (w *world) Save() (func() error, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.saveErr != nil {
		return nil, w.saveErr
	}
	saved := w.text
	w.logf("save " + saved)
	return func() error {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.restoreErr != nil {
			return w.restoreErr
		}
		w.text = saved
		w.changes++
		w.logf("restore " + saved)
		return nil
	}, nil
}

func (w *world) SetText(text string, transient bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.setErr != nil {
		return w.setErr
	}
	w.text, w.marked = text, transient
	w.changes++
	if transient {
		w.logf("set transient " + text)
	} else {
		w.logf("set " + text)
	}
	return nil
}

func (w *world) ChangeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.changes
}

func (w *world) Paste() error {
	w.mu.Lock()
	if w.pasteErr != nil {
		w.mu.Unlock()
		return w.pasteErr
	}
	w.logf("paste " + w.text)
	f := w.onPaste
	w.mu.Unlock()
	if f != nil {
		f()
	}
	return nil
}

func (w *world) Type(text string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.typeErr != nil {
		return w.typeErr
	}
	w.typed += text
	w.logf("type " + text)
	return nil
}

func (w *world) PressReturn() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.returnErr != nil {
		return w.returnErr
	}
	w.logf("return")
	return nil
}

func (w *world) log() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.events)
}

// newOutputter returns an Outputter over w whose sleeps are logged instead
// of slept.
func newOutputter(w *world) *Outputter {
	o := New(w, w, nil)
	o.sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		w.mu.Lock()
		w.logf("sleep " + d.String())
		w.mu.Unlock()
		return nil
	}
	return o
}

func defaults() Options { return OptionsFrom(config.Defaults()) }

func wantLog(t *testing.T, w *world, want ...string) {
	t.Helper()
	if got := w.log(); !slices.Equal(got, want) {
		t.Errorf("events:\n  got  %q\n  want %q", got, want)
	}
}

func TestOptionsFrom(t *testing.T) {
	s := config.Defaults()
	want := Options{Method: config.PasteClipboard, Clipboard: config.ClipboardRestore, DelayBefore: 60 * time.Millisecond, DelayAfter: 60 * time.Millisecond}
	if got := OptionsFrom(s); got != want {
		t.Errorf("defaults: %+v, want %+v", got, want)
	}
	s.PasteMethod, s.ClipboardHandling, s.AutoSubmit, s.AppendTrailingSpace = config.PasteDirect, config.ClipboardKeep, true, true
	s.PasteDelayMS, s.PasteDelayAfterMS = 10, 250
	want = Options{Method: config.PasteDirect, Clipboard: config.ClipboardKeep, DelayBefore: 10 * time.Millisecond, DelayAfter: 250 * time.Millisecond, AutoSubmit: true, TrailingSpace: true}
	if got := OptionsFrom(s); got != want {
		t.Errorf("custom: %+v, want %+v", got, want)
	}
}

func TestPasteRestoresTheClipboard(t *testing.T) {
	w := &world{text: "user's copy"}
	if err := newOutputter(w).Deliver(context.Background(), "Ask not.", defaults()); err != nil {
		t.Fatal(err)
	}
	wantLog(t, w, "save user's copy", "set transient Ask not.", "sleep 60ms", "paste Ask not.", "sleep 60ms", "restore user's copy")
	if w.text != "user's copy" {
		t.Errorf("clipboard %q, want the user's copy back", w.text)
	}
}

func TestPasteRestoresAnEmptyClipboard(t *testing.T) {
	w := &world{}
	if err := newOutputter(w).Deliver(context.Background(), "Ask not.", defaults()); err != nil {
		t.Fatal(err)
	}
	if w.text != "" {
		t.Errorf("clipboard %q; it was empty and must be empty again", w.text)
	}
}

func TestPasteKeepsTheTranscriptWhenAsked(t *testing.T) {
	w := &world{text: "user's copy"}
	opts := defaults()
	opts.Clipboard = config.ClipboardKeep
	if err := newOutputter(w).Deliver(context.Background(), "Ask not.", opts); err != nil {
		t.Fatal(err)
	}
	// Not transient: the user wants it in their clipboard history.
	wantLog(t, w, "set Ask not.", "sleep 60ms", "paste Ask not.", "sleep 60ms")
	if w.text != "Ask not." {
		t.Errorf("clipboard %q", w.text)
	}
}

func TestTrailingSpaceAndAutoSubmit(t *testing.T) {
	w := &world{text: "x"}
	opts := defaults()
	opts.TrailingSpace, opts.AutoSubmit = true, true
	if err := newOutputter(w).Deliver(context.Background(), "ls", opts); err != nil {
		t.Fatal(err)
	}
	wantLog(t, w, "save x", "set transient ls ", "sleep 60ms", "paste ls ", "sleep 60ms", "restore x", "sleep 50ms", "return")
}

func TestDirectTyping(t *testing.T) {
	w := &world{text: "user's copy"}
	opts := defaults()
	opts.Method, opts.AutoSubmit = config.PasteDirect, true
	if err := newOutputter(w).Deliver(context.Background(), "Grüße, 世界 👋", opts); err != nil {
		t.Fatal(err)
	}
	wantLog(t, w, "type Grüße, 世界 👋", "sleep 50ms", "return")
	if w.text != "user's copy" || w.changes != 0 {
		t.Errorf("direct typing touched the clipboard: %q, %d changes", w.text, w.changes)
	}
}

func TestCopyOnly(t *testing.T) {
	w := &world{text: "user's copy"}
	opts := defaults()
	opts.Method, opts.AutoSubmit = config.PasteNone, true
	if err := newOutputter(w).Deliver(context.Background(), "Ask not.", opts); err != nil {
		t.Fatal(err)
	}
	wantLog(t, w, "set Ask not.") // no keystrokes at all, auto-submit included
}

func TestNothingToDeliver(t *testing.T) {
	w := &world{text: "user's copy"}
	for _, text := range []string{"", "  \n"} {
		if err := newOutputter(w).Deliver(context.Background(), text, defaults()); err != nil {
			t.Fatal(err)
		}
	}
	wantLog(t, w)
}

// A failed paste still puts the clipboard back, and the error says why it
// failed.
func TestFailedPasteRestores(t *testing.T) {
	w := &world{text: "user's copy", pasteErr: ErrNoPermission}
	opts := defaults()
	opts.AutoSubmit = true
	err := newOutputter(w).Deliver(context.Background(), "Ask not.", opts)
	var oerr *Error
	if !errors.As(err, &oerr) || oerr.Op != "paste" || !errors.Is(err, ErrNoPermission) {
		t.Fatalf("err = %v, want a paste error matching ErrNoPermission", err)
	}
	wantLog(t, w, "save user's copy", "set transient Ask not.", "sleep 60ms", "restore user's copy")
	if !strings.Contains(err.Error(), "output: paste: output: no permission") {
		t.Errorf("message %q", err)
	}
}

// If something else writes to the clipboard while the paste is under way,
// such as the user copying, that newer copy wins.
func TestNewerCopyIsNotOverwritten(t *testing.T) {
	w := &world{text: "old copy"}
	w.onPaste = func() {
		if err := w.SetText("copied meanwhile", false); err != nil {
			t.Error(err)
		}
	}
	if err := newOutputter(w).Deliver(context.Background(), "Ask not.", defaults()); err != nil {
		t.Fatal(err)
	}
	if w.text != "copied meanwhile" {
		t.Errorf("clipboard %q, want the newer copy", w.text)
	}
}

func TestCancelledBeforeThePaste(t *testing.T) {
	w := &world{text: "user's copy"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := newOutputter(w).Deliver(ctx, "Ask not.", defaults())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	wantLog(t, w, "save user's copy", "set transient Ask not.", "restore user's copy")
}

func TestStepFailures(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name string
		set  func(*world)
		opts func(*Options)
		op   string
		text string // the clipboard afterwards
	}{
		{"save", func(w *world) { w.saveErr = boom }, nil, "save clipboard", "user's copy"},
		{"copy", func(w *world) { w.setErr = boom }, nil, "copy", "user's copy"},
		{"restore", func(w *world) { w.restoreErr = boom }, nil, "restore clipboard", "Ask not."},
		{"type", func(w *world) { w.typeErr = boom }, func(o *Options) { o.Method = config.PasteDirect }, "type", "user's copy"},
		{"submit", func(w *world) { w.returnErr = boom }, func(o *Options) { o.AutoSubmit = true }, "submit", "user's copy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &world{text: "user's copy"}
			tc.set(w)
			opts := defaults()
			if tc.opts != nil {
				tc.opts(&opts)
			}
			err := newOutputter(w).Deliver(context.Background(), "Ask not.", opts)
			var oerr *Error
			if !errors.As(err, &oerr) || oerr.Op != tc.op || !errors.Is(err, boom) {
				t.Fatalf("err = %v, want the %s step to fail with boom", err, tc.op)
			}
			if w.text != tc.text {
				t.Errorf("clipboard %q, want %q", w.text, tc.text)
			}
		})
	}
}

// Deliveries do not overlap: a second one waits for the first to restore
// the clipboard.
func TestDeliveriesAreSerialised(t *testing.T) {
	w := &world{text: "user's copy"}
	o := New(w, w, nil)
	o.sleep = func(context.Context, time.Duration) error { time.Sleep(time.Millisecond); return nil }
	var wg sync.WaitGroup
	for _, text := range []string{"one", "two", "three"} {
		wg.Go(func() {
			if err := o.Deliver(context.Background(), text, defaults()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	log := w.log()
	for i := 0; i < len(log); i += 4 {
		if !strings.HasPrefix(log[i], "save user's copy") || log[i+3] != "restore user's copy" {
			t.Fatalf("deliveries overlap: %q", log)
		}
	}
	if w.text != "user's copy" {
		t.Errorf("clipboard %q", w.text)
	}
}
