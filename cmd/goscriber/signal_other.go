//go:build !unix

package main

// Windows has no SIGUSR2.
func ignoreToggleSignal() {}
