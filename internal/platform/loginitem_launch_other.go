//go:build !darwin

package platform

// WatchLaunch does nothing outside macOS: the login item starts Goscriber
// with AtLoginFlag.
func WatchLaunch() {}

// LaunchedAtLogin is false outside macOS, where AtLoginFlag says so
// instead.
func LaunchedAtLogin() bool { return false }
