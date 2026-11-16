//go:build unix

package app

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/opragmatico/goscriber/internal/pipeline"
)

// toggleSignal starts and stops dictation, as in Handy: Wayland lets no app
// take a global shortcut, so the desktop's own shortcut runs
// `pkill -USR2 -x goscriber`. The executable ignores it until the app
// listens (cmd/goscriber), so the engine workers survive it.
const toggleSignal = syscall.SIGUSR2

// listenToggleSignal toggles dictation on each SIGUSR2 until the app stops.
func (sh *shell) listenToggleSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, toggleSignal)
	sh.following.Go(func() {
		defer signal.Stop(ch)
		for {
			select {
			case <-sh.quit:
				return
			case <-ch:
				sh.rt.Log.Debug("SIGUSR2: toggling dictation")
				sh.dictate(pipeline.ActionToggle)
			}
		}
	})
}
