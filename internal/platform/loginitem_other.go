//go:build !darwin && !linux && !windows

package platform

var errLoginItemOther = unsupportedError("launching at login is not supported on this system")

// LoginItemSupported says that this system has no login item support.
func LoginItemSupported() error { return errLoginItemOther }

// LoginItemEnabled is always false here.
func LoginItemEnabled() (bool, error) { return false, errLoginItemOther }

// SetLoginItem fails here.
func SetLoginItem(bool) error { return errLoginItemOther }
