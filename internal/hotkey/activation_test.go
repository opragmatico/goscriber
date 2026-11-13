package hotkey

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
)

// step is one synthetic event: "down@0", "up@500" (milliseconds), "cancel",
// "rec" or "idle" (SetRecording true or false), with the action it must
// produce.
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
			got = a.Press(now)
		case "up":
			got = a.Release(now)
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

func TestActionString(t *testing.T) {
	for a, want := range map[Action]string{ActionNone: "none", ActionStart: "start", ActionStop: "stop", ActionCancel: "cancel"} {
		if a.String() != want {
			t.Errorf("%d: %q", a, a.String())
		}
	}
}
