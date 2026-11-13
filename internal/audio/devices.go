package audio

import (
	"errors"
	"strings"
)

// ErrNoBackend means this build cannot reach audio devices: it was built
// without CGo, which malgo needs.
var ErrNoBackend = errors.New("audio devices are not available in this build (it needs CGo)")

// Device is an audio input or output device.
type Device struct {
	// ID identifies the device across runs and reconnections: the platform's
	// own identifier (a CoreAudio UID on macOS), hex-encoded.
	ID string
	// Name is what the system calls the device, such as "MacBook Pro
	// Microphone". Settings store it, as Handy does.
	Name string
	// Default reports whether the system uses this device by default.
	Default bool
}

// InputDevices lists the microphones.
func InputDevices() ([]Device, error) { return systemBackend().devices(captureDevices) }

// OutputDevices lists the speakers and headphones.
func OutputDevices() ([]Device, error) { return systemBackend().devices(playbackDevices) }

type deviceKind int

const (
	captureDevices deviceKind = iota
	playbackDevices
)

// findDevice returns the device want names: by ID, then by name, then by
// name without regard to case.
func findDevice(devs []Device, want string) (Device, bool) {
	for _, match := range []func(Device) bool{
		func(d Device) bool { return d.ID == want },
		func(d Device) bool { return d.Name == want },
		func(d Device) bool { return strings.EqualFold(d.Name, want) },
	} {
		for _, d := range devs {
			if match(d) {
				return d, true
			}
		}
	}
	return Device{}, false
}

// backend reaches the audio devices: malgo in CGo builds, a synthetic one in
// tests.
type backend interface {
	devices(kind deviceKind) ([]Device, error)
	// open prepares a capture device, or the default one when id is "", to
	// deliver mono float32 samples at its native rate. onData runs on the
	// audio thread and must not block. onStop runs when the device stops on
	// its own, such as when it is unplugged.
	open(id string, onData func([]float32), onStop func()) (inputStream, error)
	// openOutput prepares a playback device, or the default one when id is
	// "", to play mono float32 samples at rate Hz; the backend converts them
	// to the device's channels and rate. fill runs on the audio thread for
	// each period, must fill out (with zeros past the end of the sound) and
	// must not block.
	openOutput(id string, rate int, fill func(out []float32)) (outputStream, error)
}

// outputStream is an open playback device.
type outputStream interface {
	start() error
	// close stops and releases the device. No fill call is running or will
	// run once it returns.
	close()
}

// inputStream is an open capture device.
type inputStream interface {
	rate() int
	start() error
	// close stops and releases the device. No onData call is running or
	// will run once it returns.
	close()
}
