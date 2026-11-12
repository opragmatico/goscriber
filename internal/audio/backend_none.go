//go:build !cgo

package audio

// systemBackend has no devices without CGo.
func systemBackend() backend { return noBackend{} }

type noBackend struct{}

func (noBackend) devices(deviceKind) ([]Device, error) { return nil, ErrNoBackend }

func (noBackend) open(string, func([]float32), func()) (inputStream, error) {
	return nil, ErrNoBackend
}
