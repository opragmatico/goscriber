//go:build unix

package main

import (
	"os/signal"
	"syscall"
)

func ignoreToggleSignal() { signal.Ignore(syscall.SIGUSR2) }
