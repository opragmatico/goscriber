//go:build linux

package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// LoginItemSupported reports whether Goscriber can launch at login, which on
// Linux it always can, through the XDG autostart directory.
func LoginItemSupported() error { return nil }

// LoginItemEnabled reports whether the desktop starts Goscriber when the
// user logs in.
func LoginItemEnabled() (bool, error) {
	x, err := newXDGAutostart()
	if err != nil {
		return false, err
	}
	return x.enabled()
}

// SetLoginItem adds Goscriber to the apps the desktop starts at login, as
// the running executable with AtLoginFlag, or removes it. Adding it again
// brings the executable's path up to date.
func SetLoginItem(on bool) error {
	x, err := newXDGAutostart()
	if err != nil {
		return err
	}
	var exe string
	if on {
		if exe, err = executable(); err != nil {
			return fmt.Errorf("platform: find Goscriber's executable: %w", err)
		}
	}
	return x.set(on, exe)
}

// newXDGAutostart is the login item in autostart under $XDG_CONFIG_HOME, or
// ~/.config.
func newXDGAutostart() (xdgAutostart, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return xdgAutostart{}, fmt.Errorf("platform: find the autostart directory: %w", err)
	}
	return xdgAutostart{dir: filepath.Join(dir, "autostart")}, nil
}
