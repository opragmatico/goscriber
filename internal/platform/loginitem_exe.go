//go:build linux || windows

package platform

import (
	"os"
	"path/filepath"
)

// executable is the path of the running Goscriber, with symbolic links
// resolved, for the login item to start: the .deb's /usr/bin/goscriber
// becomes /opt/goscriber/bin/goscriber.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}
