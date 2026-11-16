//go:build !unix

package app

import "os"

// toggleSignals toggle dictation when sent to the app; this system has no
// SIGUSR2.
var toggleSignals []os.Signal
