//go:build unix

package worker

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// protocolOutput returns a copy of stdout for the protocol and points file
// descriptor 1 at stderr, so stray prints cannot reach the supervisor.
func protocolOutput() (*os.File, error) {
	fd, err := unix.Dup(int(os.Stdout.Fd()))
	if err != nil {
		return nil, fmt.Errorf("copying stdout: %w", err)
	}
	unix.CloseOnExec(fd)
	if err := unix.Dup2(int(os.Stderr.Fd()), int(os.Stdout.Fd())); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("pointing stdout at stderr: %w", err)
	}
	return os.NewFile(uintptr(fd), "protocol"), nil
}
