//go:build unix

package app

import (
	"os"
	"syscall"
)

// toggleSignals toggle dictation when sent to the app.
var toggleSignals = []os.Signal{syscall.SIGUSR2}
