//go:build !darwin

package platform

// StatusOf returns where a permission stands: only macOS has these, so
// elsewhere it is StatusNotNeeded.
func StatusOf(Permission) PermissionStatus { return StatusNotNeeded }

// Request asks for a permission on macOS; elsewhere there is nothing to ask.
func Request(Permission) PermissionStatus { return StatusNotNeeded }
