package hotkey

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
)

// step is one synthetic event: "down@0", "up@500" (milliseconds) of the
// transcribe shortcut, "pdown@0" and "pup@500" of the post-processing
// shortcut, "cancel", or "rec" or "idle" (SetRecording true or false), with
// the action it must produce.
type step struct {
	ev   string
	want Action
}

func runSteps(t *testing.T, mode config.ActivationMode, steps []step) {
	t.Helper()
	a := &Activator{Mode: mode, HoldThreshold: 300 * time.Millisecond}
	base := time.Unix(1000, 0)
	for i, s := range steps {
		var got Action
		name, at, _ := strings.Cut(s.ev, "@")
		var ms int
		if at != "" {
			if _, err := fmt.Sscan(at, &ms); err != nil {
				t.Fatal(err)
			}
		}
		now := base.Add(time.Duration(ms) * time.Millisecond)
		switch name {
		case "down":
			got = a.Press(PurposeTranscribe, now)
		case "up":
			got = a.Release(PurposeTranscribe, now)
		case "pdown":
			got = a.Press(PurposePostProcess, now)
		case "pup":
			got = a.Release(PurposePostProcess, now)
		case "cancel":
			got = a.Cancel()
		case "rec":
			a.SetRecording(true)
		case "idle":
			a.SetRecording(false)
		default:
			t.Fatalf("bad step %q", s.ev)
		}
		if got != s.want {
			t.Fatalf("step %d (%s): %s, want %s", i, s.ev, got, s.want)
		}
	}
}

func TestToggle(t *testing.T) {
	runSteps(t, config.ActivationToggle, []step{
		{"down@0", ActionStart},
		{"down@30", ActionNone}, // key repeat
		{"up@1000", ActionNone}, // releases never stop a toggle
		{"down@3000", ActionStop},
		{"up@3100", ActionNone},
		{"down@5000", ActionStart},
		{"up@5100", ActionNone},
		{"cancel", ActionCancel},
		{"cancel", ActionNone}, // nothing left to cancel
		{"down@7000", ActionStart},
	})
}

func TestPushToTalk(t *testing.T) {
	runSteps(t, config.ActivationPushToTalk, []step{
		{"down@0", ActionStart},
		{"down@40", ActionNone}, // key repeat
		{"up@50", ActionStop},   // even a short press is push-to-talk
		{"up@60", ActionNone},   // a stray release
		{"down@1000", ActionStart},
		{"cancel", ActionCancel},
		{"up@1500", ActionNone}, // the cancelled press's release
		// A recording started from the tray stops when the shortcut is
		// pressed and released.
		{"rec", ActionNone},
		{"down@3000", ActionNone},
		{"up@3200", ActionStop},
	})
}

func TestHoldOrToggle(t *testing.T) {
	runSteps(t, config.ActivationHoldOrToggle, []step{
		// Held past the threshold: push-to-talk.
		{"down@0", ActionStart},
		{"down@100", ActionNone}, // key repeat
		{"up@300", ActionStop},   // exactly the threshold counts as held
		// A tap latches the recording until the next press.
		{"down@1000", ActionStart},
		{"up@1100", ActionNone},
		{"down@4000", ActionStop},
		{"up@4100", ActionNone}, // the stopping press's release does nothing
		// Tap, then cancel.
		{"down@5000", ActionStart},
		{"up@5050", ActionNone},
		{"cancel", ActionCancel},
		{"down@6000", ActionStart},
		{"up@6900", ActionStop},
		// A recording started from the tray stops at the next press.
		{"rec", ActionNone},
		{"down@8000", ActionStop},
		{"up@9000", ActionNone},
	})
}

// Dictation refusing to start (no model, say) or ending the recording on its
// own puts the shortcut back to idle; the held press no longer counts.
func TestSetRecordingOverridesTheShortcut(t *testing.T) {
	for _, mode := range []config.ActivationMode{config.ActivationToggle, config.ActivationPushToTalk, config.ActivationHoldOrToggle} {
		t.Run(string(mode)+" refused", func(t *testing.T) {
			runSteps(t, mode, []step{
				{"down@0", ActionStart},
				{"idle", ActionNone},
				{"up@1000", ActionNone},
				{"down@2000", ActionStart}, // starts afresh rather than stopping
			})
		})
	}
	t.Run("hold_or_toggle ended while held", func(t *testing.T) {
		runSteps(t, config.ActivationHoldOrToggle, []step{
			{"down@0", ActionStart},
			{"rec", ActionNone}, // dictation confirms: the press still owns it
			{"up@800", ActionStop},
			{"down@1000", ActionStart},
			{"rec", ActionNone},
			{"idle", ActionNone}, // dictation stopped it (an error, the tray)
			{"up@2000", ActionNone},
		})
	})
}

// The post-processing shortcut drives dictation as the transcribe shortcut
// does, starting post-processed dictations. A recording belongs to the
// shortcut that started it: the other one does nothing until it ends.
func TestPostProcessShortcut(t *testing.T) {
	t.Run("toggle", func(t *testing.T) {
		runSteps(t, config.ActivationToggle, []step{
			{"pdown@0", ActionStartPostProcess},
			{"pdown@30", ActionNone}, // key repeat
			{"pup@100", ActionNone},
			{"down@1000", ActionNone}, // the other shortcut's recording
			{"up@1100", ActionNone},
			{"pdown@2000", ActionStop},
			{"pup@2100", ActionNone},
			{"down@3000", ActionStart},
			{"up@3100", ActionNone},
			{"pdown@4000", ActionNone}, // and the other way round
			{"pup@4100", ActionNone},
			{"down@5000", ActionStop},
			{"up@5100", ActionNone},
		})
	})
	t.Run("push_to_talk", func(t *testing.T) {
		runSteps(t, config.ActivationPushToTalk, []step{
			{"pdown@0", ActionStartPostProcess},
			{"down@100", ActionNone}, // the other key, while this one is held
			{"up@200", ActionNone},
			{"pup@1000", ActionStop},
			// Held over the other key's release: the release that counts is
			// the key's own.
			{"down@2000", ActionStart},
			{"pdown@2100", ActionNone},
			{"up@2200", ActionStop},
			{"pup@2300", ActionNone},
			{"pdown@3000", ActionStartPostProcess},
			{"cancel", ActionCancel},
			{"pup@3500", ActionNone},
		})
	})
	t.Run("hold_or_toggle", func(t *testing.T) {
		runSteps(t, config.ActivationHoldOrToggle, []step{
			{"pdown@0", ActionStartPostProcess},
			{"pup@100", ActionNone}, // a tap latches it
			{"down@1000", ActionNone},
			{"up@1100", ActionNone},
			{"pdown@2000", ActionStop},
			{"pup@2100", ActionNone},
			{"pdown@3000", ActionStartPostProcess},
			{"pup@3500", ActionStop}, // held
		})
	})
	// A recording started from the tray belongs to neither shortcut, so
	// either stops it; so does one dictation ended and started again.
	t.Run("started elsewhere", func(t *testing.T) {
		runSteps(t, config.ActivationToggle, []step{
			{"rec", ActionNone},
			{"pdown@0", ActionStop},
			{"pup@100", ActionNone},
			{"down@1000", ActionStart},
			{"up@1100", ActionNone},
			{"idle", ActionNone}, // dictation stopped it (an error, the tray)
			{"rec", ActionNone},  // and the tray started another
			{"pdown@2000", ActionStop},
		})
	})
	// A refused post-processed start leaves both shortcuts free.
	t.Run("refused", func(t *testing.T) {
		runSteps(t, config.ActivationPushToTalk, []step{
			{"pdown@0", ActionStartPostProcess},
			{"idle", ActionNone},
			{"pup@100", ActionNone},
			{"down@1000", ActionStart},
		})
	})
}

func TestActionString(t *testing.T) {
	for a, want := range map[Action]string{
		ActionNone: "none", ActionStart: "start", ActionStop: "stop", ActionCancel: "cancel",
		ActionStartPostProcess: "start_post_process",
	} {
		if a.String() != want {
			t.Errorf("%d: %q", a, a.String())
		}
	}
}
