//go:build !darwin

package platform

// SecureInputEnabled is a macOS concept; elsewhere it is always false.
func SecureInputEnabled() bool { return false }

// SecureInputHolder is a macOS concept; elsewhere it finds nothing.
func SecureInputHolder() (pid int, name string, ok bool) { return 0, "", false }
