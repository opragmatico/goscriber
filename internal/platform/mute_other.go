//go:build !darwin

package platform

import "errors"

// ErrNoMuteControl means the output device has no mute switch Goscriber can
// set.
var ErrNoMuteControl = errors.New("the output device has no mute control")

// MuteOutput mutes the system's default output on macOS. Elsewhere muting is
// not supported yet, and it fails.
func MuteOutput() (restore func() error, err error) {
	return nil, errors.New("muting other audio while recording is supported on macOS only")
}
