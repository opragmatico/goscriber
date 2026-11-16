//go:build !unix

package app

// listenToggleSignal does nothing: Windows has no SIGUSR2.
func (sh *shell) listenToggleSignal() {}
