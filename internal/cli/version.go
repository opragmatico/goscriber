package cli

import "runtime/debug"

// ResolveVersion returns the version stamped at link time when there is one,
// then the module version the Go toolchain recorded in the build info (which
// includes the VCS revision for builds inside a git checkout), and finally
// "dev".
func ResolveVersion(stamped string) string {
	if stamped != "" {
		return stamped
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}
