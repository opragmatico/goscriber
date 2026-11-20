//go:build !darwin && !linux && !windows

package llm

// TotalMemory is the computer's physical memory in bytes, or 0 when it
// cannot be read, as on this OS.
func TotalMemory() uint64 { return 0 }
