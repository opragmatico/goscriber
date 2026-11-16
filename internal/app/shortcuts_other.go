//go:build !linux

package app

import "context"

// openDesktopShortcuts is nil: only Linux desktops have a global shortcuts
// portal.
var openDesktopShortcuts func(context.Context, desktopShortcutsOptions) (desktopShortcuts, error)
