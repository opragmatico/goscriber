package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/opragmatico/goscriber/internal/instance"
	"github.com/opragmatico/goscriber/internal/pipeline"
	"github.com/opragmatico/goscriber/internal/ui"
)

// errQuitting answers commands that arrive while the app shuts down.
var errQuitting = errors.New("the app is quitting")

// serveControl answers the commands later launches send on the control
// socket, until the app stops. It runs from start.
func (sh *shell) serveControl() {
	inst := sh.opts.Instance
	if inst == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	sh.stopControl = cancel
	sh.following.Go(func() {
		sh.rt.Log.Info("remote control listening", "socket", inst.SocketPath())
		if err := inst.Serve(ctx, sh.control); err != nil {
			sh.rt.Log.Warn("remote control is unavailable: goscriber --toggle-transcription and a second launch cannot reach this app", "err", err)
		}
	})
}

// control runs a command from the control socket. Like the tray's items it
// only hands the action on, so it never blocks.
func (sh *shell) control(cmd instance.Command) error {
	select {
	case <-sh.quit:
		return errQuitting
	default:
	}
	sh.rt.Log.Info("remote control", "command", string(cmd))
	switch cmd {
	case instance.Toggle:
		sh.dictate(pipeline.ActionToggle)
	case instance.Cancel:
		sh.dictate(pipeline.ActionCancel)
	case instance.Show:
		sh.do(sh.showWindow)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
	return nil
}

// showWindow answers a second launch: the settings window comes to the front
// as it is when it is open, and otherwise opens as a launch would open it,
// on onboarding while that is not over.
func (sh *shell) showWindow() {
	if sh.settings.Visible() {
		sh.settings.Raise()
		return
	}
	if step, ok := pendingOnboarding(sh.app.Preferences(), sh.rt.Settings.Get()); ok {
		sh.settings.ShowOnboarding(step)
		return
	}
	sh.settings.Show(ui.PageGeneral)
}

// watchToggleSignal toggles dictation on SIGUSR2, as Handy does, until the
// app stops. The signal is subscribed to before start returns, so one sent
// as soon as the app is up toggles rather than ending the process.
func (sh *shell) watchToggleSignal() {
	if len(toggleSignals) == 0 {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, toggleSignals...)
	sh.following.Go(func() {
		defer signal.Stop(ch)
		for {
			select {
			case <-sh.quit:
				return
			case sig := <-ch:
				sh.rt.Log.Info("toggling dictation", "signal", sig.String())
				sh.dictate(pipeline.ActionToggle)
			}
		}
	})
}
