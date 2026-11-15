//go:build !unix

package worker

import "os"

// protocolOutput is stdout itself: the native engine logs to stderr.
func protocolOutput() (*os.File, error) { return os.Stdout, nil }
