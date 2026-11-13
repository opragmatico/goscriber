package output

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
)

// ErrUnsupported is what Clipboard and Keyboard implementations return on
// an OS they do not support yet.
var ErrUnsupported = errors.New("output: not supported on this OS")

// Clipboard is the system clipboard.
type Clipboard interface {
	// Save captures everything on the clipboard, in every format, and
	// returns a function that puts it back. An empty clipboard is restored
	// empty.
	Save() (restore func() error, err error)
	// SetText replaces the clipboard's contents with text. Transient text
	// is marked for clipboard managers to skip (nspasteboard.org), because
	// it is about to be replaced by what was there before.
	SetText(text string, transient bool) error
	// ChangeCount is a number that changes whenever anything writes to the
	// clipboard.
	ChangeCount() int
}

// Keyboard sends synthetic key presses to the focused app. Implementations
// report a missing permission with an error that matches
// ErrNoPermission.
type Keyboard interface {
	// Paste sends the paste shortcut, Cmd+V on macOS and Ctrl+V elsewhere,
	// with the key that types "v" in the active keyboard layout.
	Paste() error
	// Type types text as if it were entered on the keyboard.
	Type(text string) error
	// PressReturn presses Return.
	PressReturn() error
}

// ErrNoPermission means the OS refused to send key presses to other apps:
// on macOS Goscriber needs the Accessibility permission.
var ErrNoPermission = errors.New("output: no permission to send key presses to other apps")

// submitDelay is the pause between the transcript and the Return of
// auto-submit, so the app has handled the text first.
const submitDelay = 50 * time.Millisecond

// Options say how a transcript is delivered.
type Options struct {
	Method config.PasteMethod
	// Clipboard says whether a paste puts the user's clipboard back
	// afterwards (restore) or leaves the transcript on it (keep).
	Clipboard config.ClipboardHandling
	// DelayBefore is the pause between writing the clipboard and sending
	// the paste shortcut, for apps that read the clipboard late.
	DelayBefore time.Duration
	// DelayAfter is the pause between the paste shortcut and restoring the
	// clipboard, so the app has read the transcript first.
	DelayAfter time.Duration
	// AutoSubmit presses Return after the transcript, to send a chat
	// message or run a command.
	AutoSubmit bool
	// TrailingSpace adds a space after the transcript, so the next
	// dictation does not run into it.
	TrailingSpace bool
}

// OptionsFrom returns the Options the settings describe.
func OptionsFrom(s config.Settings) Options {
	return Options{
		Method:        s.PasteMethod,
		Clipboard:     s.ClipboardHandling,
		DelayBefore:   time.Duration(s.PasteDelayMS) * time.Millisecond,
		DelayAfter:    time.Duration(s.PasteDelayAfterMS) * time.Millisecond,
		AutoSubmit:    s.AutoSubmit,
		TrailingSpace: s.AppendTrailingSpace,
	}
}

// Error is a delivery that failed at one step.
type Error struct {
	// Op is the step: "save clipboard", "copy", "paste", "type", "submit"
	// or "restore clipboard".
	Op  string
	Err error
}

func (e *Error) Error() string { return fmt.Sprintf("output: %s: %v", e.Op, e.Err) }

func (e *Error) Unwrap() error { return e.Err }

// Outputter delivers transcripts to the focused app. Deliveries are
// serialised: overlapping ones would restore each other's transcript as the
// user's clipboard.
type Outputter struct {
	clip  Clipboard
	kb    Keyboard
	log   *slog.Logger
	sleep func(context.Context, time.Duration) error

	mu sync.Mutex
}

// New returns an Outputter over the system's clipboard and keyboard. A nil
// log means slog's default logger.
func New(clip Clipboard, kb Keyboard, log *slog.Logger) *Outputter {
	if log == nil {
		log = slog.Default()
	}
	return &Outputter{clip: clip, kb: kb, log: log, sleep: sleepCtx}
}

// Deliver puts text into the focused app as opts says:
//
//   - paste: save the clipboard, write the transcript, wait DelayBefore,
//     send the paste shortcut, wait DelayAfter, then put the clipboard back
//     (unless opts.Clipboard is keep, or something else wrote to it in the
//     meantime) and press Return for auto-submit.
//   - direct: type the transcript; the clipboard is not touched.
//   - none: copy the transcript to the clipboard and leave it there.
//
// Deliver blocks for the delays, so call it off the main thread. The
// clipboard is put back even when the paste shortcut fails. Cancelling ctx
// during the first delay skips the paste; the clipboard is still restored.
// Errors are *Error values.
func (o *Outputter) Deliver(ctx context.Context, text string, opts Options) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if opts.TrailingSpace {
		text += " "
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	switch opts.Method {
	case config.PasteNone:
		if err := o.clip.SetText(text, false); err != nil {
			return &Error{Op: "copy", Err: err}
		}
		return nil
	case config.PasteDirect:
		if err := o.kb.Type(text); err != nil {
			return &Error{Op: "type", Err: err}
		}
		return o.submit(ctx, opts)
	default:
		return o.paste(ctx, text, opts)
	}
}

func (o *Outputter) paste(ctx context.Context, text string, opts Options) error {
	keep := opts.Clipboard == config.ClipboardKeep
	var restore func() error
	if !keep {
		var err error
		if restore, err = o.clip.Save(); err != nil {
			// Writing now would lose what the user had copied.
			return &Error{Op: "save clipboard", Err: err}
		}
	}
	if err := o.clip.SetText(text, !keep); err != nil {
		_ = o.restore(restore, -1) // best effort; the copy failure is the news
		return &Error{Op: "copy", Err: err}
	}
	written := o.clip.ChangeCount()

	var pasteErr error
	if err := o.sleep(ctx, opts.DelayBefore); err != nil {
		pasteErr = &Error{Op: "paste", Err: err}
	} else if err := o.kb.Paste(); err != nil {
		pasteErr = &Error{Op: "paste", Err: err}
	} else {
		// The app reads the clipboard when it handles the shortcut; give
		// it time before the clipboard changes again. Not cancellable: a
		// restore that came too soon would paste the user's old clipboard.
		_ = o.sleep(context.Background(), opts.DelayAfter)
	}
	restoreErr := o.restore(restore, written)
	if pasteErr != nil {
		return pasteErr
	}
	if restoreErr != nil {
		return &Error{Op: "restore clipboard", Err: restoreErr}
	}
	return o.submit(ctx, opts)
}

// restore puts the saved clipboard back, unless something wrote to the
// clipboard since the transcript did (written is its change count; -1
// skips the check): a newer copy wins over an older one.
func (o *Outputter) restore(restore func() error, written int) error {
	if restore == nil {
		return nil
	}
	if written >= 0 && o.clip.ChangeCount() != written {
		o.log.Debug("clipboard changed during the paste; leaving it as it is")
		return nil
	}
	return restore()
}

func (o *Outputter) submit(ctx context.Context, opts Options) error {
	if !opts.AutoSubmit {
		return nil
	}
	if err := o.sleep(ctx, submitDelay); err != nil {
		return &Error{Op: "submit", Err: err}
	}
	if err := o.kb.PressReturn(); err != nil {
		return &Error{Op: "submit", Err: err}
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
