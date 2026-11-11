//go:build !darwin

package platform

// SetDockIcon is a macOS concept; elsewhere it does nothing.
func SetDockIcon(bool) {}
