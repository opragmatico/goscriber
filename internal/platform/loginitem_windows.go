//go:build windows

package platform

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// The Run key holds the programs Windows starts at the user's login, by
// value name. Task Manager's Startup apps and Settings → Apps → Startup
// turn one off without removing it, by an entry of the same name under
// StartupApproved whose first byte is odd.
const (
	runKey      = `Software\Microsoft\Windows\CurrentVersion\Run`
	approvedKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
	runValue    = "Goscriber"
)

// LoginItemSupported reports whether Goscriber can launch at login, which on
// Windows it always can, through the Run registry key.
func LoginItemSupported() error { return nil }

// LoginItemEnabled reports whether Windows starts Goscriber when the user
// logs in: the Run value is there and Startup apps has not turned it off.
func LoginItemEnabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("platform: read the login item: %w", err)
	}
	defer k.Close()
	if _, _, err := k.GetStringValue(runValue); errors.Is(err, registry.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("platform: read the login item: %w", err)
	}
	return !turnedOffInStartupApps(), nil
}

func turnedOffInStartupApps() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	b, _, err := k.GetBinaryValue(runValue)
	return err == nil && len(b) > 0 && b[0]&1 == 1
}

// SetLoginItem adds Goscriber to the programs Windows starts at login, as
// the running executable with AtLoginFlag, or removes it. Adding it again
// brings the executable's path up to date. Either way Startup apps forgets
// having turned it off, so the setting is what counts.
func SetLoginItem(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("platform: open the Run key: %w", err)
	}
	defer k.Close()
	if on {
		exe, err := executable()
		if err != nil {
			return fmt.Errorf("platform: find Goscriber's executable: %w", err)
		}
		if err := k.SetStringValue(runValue, windowsRunCommand(exe)); err != nil {
			return fmt.Errorf("platform: write the login item: %w", err)
		}
	} else if err := k.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("platform: remove the login item: %w", err)
	}
	if a, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.SET_VALUE); err == nil {
		_ = a.DeleteValue(runValue) // usually absent
		a.Close()
	}
	return nil
}
