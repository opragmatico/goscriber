//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly)

package instance

import "context"

// Instance is the running app's hold on being the only Goscriber. On this
// system there is none.
type Instance struct{}

// Acquire returns ErrUnsupported on this system.
func Acquire(string) (*Instance, error) { return nil, ErrUnsupported }

// SocketPath returns "" on this system.
func (*Instance) SocketPath() string { return "" }

// Serve returns ErrUnsupported on this system.
func (*Instance) Serve(context.Context, Handler) error { return ErrUnsupported }

// Close does nothing on this system.
func (*Instance) Close() error { return nil }

// Send returns ErrUnsupported on this system.
func Send(context.Context, string, Command) error { return ErrUnsupported }
