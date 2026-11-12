//go:build !darwin && !linux && !windows

package models

import "errors"

// freeSpace cannot tell on this platform; the download then goes ahead
// without the check.
func freeSpace(string) (uint64, error) {
	return 0, errors.ErrUnsupported
}
