package llm

import "golang.org/x/sys/unix"

// TotalMemory is the computer's physical memory in bytes, or 0 when it
// cannot be read.
func TotalMemory() uint64 {
	var info unix.Sysinfo_t
	if err := unix.Sysinfo(&info); err != nil {
		return 0
	}
	return uint64(info.Totalram) * uint64(info.Unit)
}
