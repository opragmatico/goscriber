//go:build cgo

package audio

import (
	"encoding/hex"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/gen2brain/malgo"
)

// periodMillis is how much audio each device callback carries.
const periodMillis = 10

var (
	sysOnce    sync.Once
	sysBackend *malgoBackend
)

// systemBackend is malgo with the platform's default audio backends
// (CoreAudio on macOS, WASAPI on Windows, PulseAudio or ALSA on Linux).
func systemBackend() backend {
	sysOnce.Do(func() { sysBackend = &malgoBackend{} })
	return sysBackend
}

// malgoBackend reaches devices through one miniaudio context, created on
// first use and kept for the process's life, as miniaudio recommends.
type malgoBackend struct {
	backends []malgo.Backend // nil: the platform's defaults

	mu  sync.Mutex
	ctx *malgo.AllocatedContext
	err error
}

func (b *malgoBackend) context() (*malgo.AllocatedContext, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ctx == nil && b.err == nil {
		b.ctx, b.err = malgo.InitContext(b.backends, malgo.ContextConfig{}, nil)
		if b.err != nil {
			b.err = fmt.Errorf("audio: init: %w", b.err)
		}
	}
	return b.ctx, b.err
}

func (b *malgoBackend) devices(kind deviceKind) ([]Device, error) {
	ctx, err := b.context()
	if err != nil {
		return nil, err
	}
	typ := malgo.Capture
	if kind == playbackDevices {
		typ = malgo.Playback
	}
	infos, err := ctx.Devices(typ)
	if err != nil {
		return nil, fmt.Errorf("audio: list devices: %w", err)
	}
	devs := make([]Device, len(infos))
	for i := range infos {
		devs[i] = Device{ID: infos[i].ID.String(), Name: infos[i].Name(), Default: infos[i].IsDefault != 0}
	}
	return devs, nil
}

func (b *malgoBackend) open(id string, onData func([]float32), onStop func()) (inputStream, error) {
	ctx, err := b.context()
	if err != nil {
		return nil, err
	}
	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatF32
	cfg.Capture.Channels = 1 // miniaudio mixes the device's channels down
	cfg.SampleRate = 0       // the device's native rate: no resampling here
	cfg.PeriodSizeInMilliseconds = periodMillis
	cfg.Alsa.NoMMap = 1

	// miniaudio copies the ID during init, so a pinned Go value will do.
	var pin runtime.Pinner
	defer pin.Unpin()
	if id != "" {
		p, err := deviceID(id)
		if err != nil {
			return nil, err
		}
		pin.Pin(p)
		cfg.Capture.DeviceID = unsafe.Pointer(p)
	}

	dev, err := malgo.InitDevice(ctx.Context, cfg, malgo.DeviceCallbacks{
		// Runs on miniaudio's thread: hand the samples over and return.
		Data: func(_, in []byte, frames uint32) {
			if n := int(frames); n > 0 && len(in) >= 4*n {
				onData(unsafe.Slice((*float32)(unsafe.Pointer(&in[0])), n))
			}
		},
		Stop: onStop,
	})
	if err != nil {
		return nil, err
	}
	return &malgoStream{dev: dev}, nil
}

func (b *malgoBackend) openOutput(id string, rate int, fill func([]float32)) (outputStream, error) {
	ctx, err := b.context()
	if err != nil {
		return nil, err
	}
	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.Playback.Format = malgo.FormatF32
	cfg.Playback.Channels = 1 // miniaudio spreads it over the device's channels
	cfg.SampleRate = uint32(rate)
	cfg.PeriodSizeInMilliseconds = periodMillis
	cfg.Alsa.NoMMap = 1

	var pin runtime.Pinner
	defer pin.Unpin()
	if id != "" {
		p, err := deviceID(id)
		if err != nil {
			return nil, err
		}
		pin.Pin(p)
		cfg.Playback.DeviceID = unsafe.Pointer(p)
	}

	dev, err := malgo.InitDevice(ctx.Context, cfg, malgo.DeviceCallbacks{
		// Runs on miniaudio's thread.
		Data: func(out, _ []byte, frames uint32) {
			if n := int(frames); n > 0 && len(out) >= 4*n {
				fill(unsafe.Slice((*float32)(unsafe.Pointer(&out[0])), n))
			}
		},
	})
	if err != nil {
		return nil, err
	}
	return &malgoStream{dev: dev}, nil
}

// deviceID decodes a Device.ID for miniaudio.
func deviceID(id string) (*malgo.DeviceID, error) {
	raw, err := hex.DecodeString(id)
	var devID malgo.DeviceID
	if err != nil || len(raw) > len(devID) {
		return nil, fmt.Errorf("audio: bad device ID %q", id)
	}
	p := new(malgo.DeviceID)
	copy(p[:], raw)
	return p, nil
}

type malgoStream struct {
	dev  *malgo.Device
	once sync.Once
}

func (s *malgoStream) rate() int    { return int(s.dev.SampleRate()) }
func (s *malgoStream) start() error { return s.dev.Start() }

// close stops the device and waits for its callback to return.
func (s *malgoStream) close() { s.once.Do(s.dev.Uninit) }
