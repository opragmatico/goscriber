package hotkey

import (
	"time"

	"github.com/opragmatico/goscriber/internal/config"
)

// Action is what a shortcut event asks dictation to do.
type Action int

// Actions.
const (
	ActionNone   Action = iota
	ActionStart         // start recording
	ActionStop          // stop recording and transcribe
	ActionCancel        // stop recording and discard it
	// ActionStartPostProcess starts recording a dictation whose transcript
	// is post-processed whatever llm_enabled says: the post-processing
	// shortcut's start.
	ActionStartPostProcess
)

func (a Action) String() string {
	switch a {
	case ActionStart:
		return "start"
	case ActionStop:
		return "stop"
	case ActionCancel:
		return "cancel"
	case ActionStartPostProcess:
		return "start_post_process"
	default:
		return "none"
	}
}

// Activator turns the key-down and key-up events of the shortcuts that
// start dictation (the transcribe and post-processing shortcuts), and the
// cancel shortcut's key-downs, into actions, following an activation mode:
//
//   - toggle: each press starts or stops a recording; releases do nothing.
//   - push-to-talk: a press starts a recording and its release stops it.
//   - hold-or-toggle: a press starts a recording. Released after at least
//     HoldThreshold it was push-to-talk, and the release stops the
//     recording; released sooner it was a tap, and the recording goes on
//     until the next press.
//
// A recording belongs to the shortcut that started it, as in Handy: the
// other shortcut does nothing until it ends. A recording started without a
// shortcut (the tray menu, the command line) belongs to neither, so either
// stops it.
//
// Key repeat is ignored: a press while the key is already down does
// nothing, and so does a press of one shortcut while the other is held.
// The Activator tracks whether a recording is in progress, but dictation
// may also start or end one on its own (the tray menu, an error, another
// cancel): report that with SetRecording. An Activator is not safe for
// concurrent use; the Manager drives it from one goroutine.
type Activator struct {
	Mode          config.ActivationMode
	HoldThreshold time.Duration

	recording bool      // a recording is in progress
	owner     Purpose   // the shortcut that started the recording, or "" when none did
	held      Purpose   // the shortcut whose key is down, or ""
	downAt    time.Time // when it was pressed
	ownsPress bool      // the current press started the recording, so its release may stop it
}

// Recording reports whether the Activator believes a recording is in
// progress.
func (a *Activator) Recording() bool { return a.recording }

// Press handles a key-down at now of the shortcut for p, PurposeTranscribe
// or PurposePostProcess.
func (a *Activator) Press(p Purpose, now time.Time) Action {
	if a.held != "" {
		return ActionNone // key repeat, or the other shortcut while this one is held
	}
	if a.recording && a.owner != "" && a.owner != p {
		return ActionNone // the other shortcut's recording
	}
	a.held, a.downAt = p, now
	switch a.Mode {
	case config.ActivationPushToTalk:
		a.ownsPress = true // its release stops even a recording started elsewhere
		if a.recording {
			return ActionNone
		}
		return a.start(p)
	case config.ActivationHoldOrToggle:
		if a.recording { // a latched tap, or a recording started elsewhere
			a.stop()
			return ActionStop
		}
		a.ownsPress = true
		return a.start(p)
	default: // toggle
		a.ownsPress = false
		if a.recording {
			a.stop()
			return ActionStop
		}
		return a.start(p)
	}
}

// start begins a recording that belongs to the shortcut for p.
func (a *Activator) start(p Purpose) Action {
	a.recording, a.owner = true, p
	if p == PurposePostProcess {
		return ActionStartPostProcess
	}
	return ActionStart
}

// stop ends the recording, which no press controls any more.
func (a *Activator) stop() {
	a.recording, a.owner, a.ownsPress = false, "", false
}

// Release handles a key-up at now of the shortcut for p.
func (a *Activator) Release(p Purpose, now time.Time) Action {
	if a.held == "" || a.held != p {
		return ActionNone
	}
	a.held = ""
	owns := a.ownsPress
	a.ownsPress = false
	if !owns || !a.recording {
		return ActionNone
	}
	switch a.Mode {
	case config.ActivationPushToTalk:
		a.stop()
		return ActionStop
	case config.ActivationHoldOrToggle:
		if now.Sub(a.downAt) >= a.HoldThreshold {
			a.stop()
			return ActionStop
		}
		return ActionNone // a tap: the recording is latched until the next press
	default:
		return ActionNone
	}
}

// Cancel handles a key-down of the cancel shortcut: it cancels a recording
// in progress and does nothing otherwise. The held key's release, if it is
// still held, then does nothing.
func (a *Activator) Cancel() Action {
	if !a.recording {
		return ActionNone
	}
	a.stop()
	return ActionCancel
}

// SetRecording tells the Activator whether a recording is in progress, when
// dictation started or ended one without the shortcut, or refused to start
// one. The press being held, if any, no longer controls the recording, and
// a recording started this way belongs to no shortcut.
func (a *Activator) SetRecording(on bool) {
	if a.recording != on {
		a.ownsPress, a.owner = false, ""
	}
	a.recording = on
}

// forget drops the press of p's key, if it is held: its shortcut changed
// or was given back to the OS, so its release will never come.
func (a *Activator) forget(p Purpose) {
	if a.held == p {
		a.held, a.ownsPress = "", false
	}
}
