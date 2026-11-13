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
)

func (a Action) String() string {
	switch a {
	case ActionStart:
		return "start"
	case ActionStop:
		return "stop"
	case ActionCancel:
		return "cancel"
	default:
		return "none"
	}
}

// Activator turns the transcribe shortcut's key-down and key-up events, and
// the cancel shortcut's key-downs, into actions, following an activation
// mode:
//
//   - toggle: each press starts or stops a recording; releases do nothing.
//   - push-to-talk: a press starts a recording and its release stops it.
//   - hold-or-toggle: a press starts a recording. Released after at least
//     HoldThreshold it was push-to-talk, and the release stops the
//     recording; released sooner it was a tap, and the recording goes on
//     until the next press.
//
// Key repeat is ignored: a press while the key is already down does
// nothing. The Activator tracks whether a recording is in progress, but
// dictation may also start or end one on its own (the tray menu, an error,
// another cancel): report that with SetRecording. An Activator is not safe
// for concurrent use; the Manager drives it from one goroutine.
type Activator struct {
	Mode          config.ActivationMode
	HoldThreshold time.Duration

	recording bool      // a recording is in progress
	down      bool      // the transcribe key is held
	downAt    time.Time // when it was pressed
	ownsPress bool      // the current press started the recording, so its release may stop it
}

// Recording reports whether the Activator believes a recording is in
// progress.
func (a *Activator) Recording() bool { return a.recording }

// Press handles a key-down of the transcribe shortcut at now.
func (a *Activator) Press(now time.Time) Action {
	if a.down {
		return ActionNone // key repeat
	}
	a.down, a.downAt = true, now
	switch a.Mode {
	case config.ActivationPushToTalk:
		a.ownsPress = true // its release stops even a recording started elsewhere
		if a.recording {
			return ActionNone
		}
		a.recording = true
		return ActionStart
	case config.ActivationHoldOrToggle:
		if a.recording { // a latched tap, or a recording started elsewhere
			a.recording, a.ownsPress = false, false
			return ActionStop
		}
		a.recording, a.ownsPress = true, true
		return ActionStart
	default: // toggle
		a.ownsPress = false
		a.recording = !a.recording
		if a.recording {
			return ActionStart
		}
		return ActionStop
	}
}

// Release handles a key-up of the transcribe shortcut at now.
func (a *Activator) Release(now time.Time) Action {
	if !a.down {
		return ActionNone
	}
	a.down = false
	owns := a.ownsPress
	a.ownsPress = false
	if !owns || !a.recording {
		return ActionNone
	}
	switch a.Mode {
	case config.ActivationPushToTalk:
		a.recording = false
		return ActionStop
	case config.ActivationHoldOrToggle:
		if now.Sub(a.downAt) >= a.HoldThreshold {
			a.recording = false
			return ActionStop
		}
		return ActionNone // a tap: the recording is latched until the next press
	default:
		return ActionNone
	}
}

// Cancel handles a key-down of the cancel shortcut: it cancels a recording
// in progress and does nothing otherwise. The transcribe key's release, if
// it is still held, then does nothing.
func (a *Activator) Cancel() Action {
	if !a.recording {
		return ActionNone
	}
	a.recording, a.ownsPress = false, false
	return ActionCancel
}

// SetRecording tells the Activator whether a recording is in progress, when
// dictation started or ended one without the shortcut, or refused to start
// one. The press being held, if any, no longer controls the recording.
func (a *Activator) SetRecording(on bool) {
	if a.recording != on {
		a.ownsPress = false
	}
	a.recording = on
}
