//go:build !((cgo && (darwin || linux)) || windows)

package hotkey

import "errors"

// ErrUnsupported is what SystemBackend's Register returns in a build that
// cannot register global shortcuts: one without cgo on macOS or Linux, or
// another OS.
var ErrUnsupported = errors.New("hotkey: global shortcuts need a cgo build on this OS")

// SystemBackend returns a backend that cannot register anything in this
// build; its Register fails with ErrUnsupported.
func SystemBackend() Backend { return noBackend{} }

type noBackend struct{}

func (noBackend) Register(Binding) (Registration, error) { return nil, ErrUnsupported }
