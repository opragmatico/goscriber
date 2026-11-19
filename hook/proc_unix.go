//go:build unix

package hook

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// stopGroup puts cmd's program at the head of a process group of its own,
// and makes cancelling cmd send SIGTERM to the whole group: a shell script's
// children too, which could otherwise run on and hold its output open. os/exec
// kills the program itself if it is still running a grace period later; the
// returned kill sends SIGKILL to whatever is left of the group.
func stopGroup(cmd *exec.Cmd) (kill func()) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return signalGroup(cmd, syscall.SIGTERM) }
	return func() { _ = signalGroup(cmd, syscall.SIGKILL) }
}

// signalGroup sends sig to the process group cmd's program leads.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
