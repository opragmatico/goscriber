//go:build darwin

package platform

/*
#cgo LDFLAGS: -framework CoreAudio
#include <CoreAudio/CoreAudio.h>

// The main element: kAudioObjectPropertyElementMain from macOS 12, formerly
// kAudioObjectPropertyElementMaster; both are 0.
#define GS_ELEMENT_MAIN 0

static OSStatus gsDefaultOutput(AudioObjectID *dev) {
	AudioObjectPropertyAddress addr = {
		kAudioHardwarePropertyDefaultOutputDevice,
		kAudioObjectPropertyScopeGlobal,
		GS_ELEMENT_MAIN,
	};
	UInt32 size = sizeof(*dev);
	return AudioObjectGetPropertyData(kAudioObjectSystemObject, &addr, 0, NULL, &size, dev);
}

static AudioObjectPropertyAddress gsMuteAddress(void) {
	AudioObjectPropertyAddress addr = {
		kAudioDevicePropertyMute,
		kAudioDevicePropertyScopeOutput,
		GS_ELEMENT_MAIN,
	};
	return addr;
}

// gsGetMute reads the device's mute switch: 0 or 1, or -1 when the device
// has none it lets us set (some HDMI and Bluetooth outputs).
static int gsGetMute(AudioObjectID dev) {
	AudioObjectPropertyAddress addr = gsMuteAddress();
	if (!AudioObjectHasProperty(dev, &addr)) {
		return -1;
	}
	Boolean settable = false;
	if (AudioObjectIsPropertySettable(dev, &addr, &settable) != noErr || !settable) {
		return -1;
	}
	UInt32 muted = 0;
	UInt32 size = sizeof(muted);
	if (AudioObjectGetPropertyData(dev, &addr, 0, NULL, &size, &muted) != noErr) {
		return -1;
	}
	return muted ? 1 : 0;
}

static OSStatus gsSetMute(AudioObjectID dev, int muted) {
	AudioObjectPropertyAddress addr = gsMuteAddress();
	UInt32 value = muted ? 1 : 0;
	return AudioObjectSetPropertyData(dev, &addr, 0, NULL, sizeof(value), &value);
}
*/
import "C"

import (
	"errors"
	"fmt"
)

// ErrNoMuteControl means the output device has no mute switch Goscriber can
// set, as with some HDMI and Bluetooth outputs.
var ErrNoMuteControl = errors.New("the output device has no mute control")

// MuteOutput mutes the system's default output device through CoreAudio and
// returns a function that puts its mute switch back as it was, on that same
// device even if the default changes in between. It is an audio.Muter.
func MuteOutput() (restore func() error, err error) {
	var dev C.AudioObjectID
	if st := C.gsDefaultOutput(&dev); st != 0 {
		return nil, fmt.Errorf("platform: find the default output: OSStatus %d", int(st))
	}
	was := C.gsGetMute(dev)
	if was < 0 {
		return nil, ErrNoMuteControl
	}
	if was == 1 {
		return func() error { return nil }, nil // already muted: leave it so
	}
	if st := C.gsSetMute(dev, 1); st != 0 {
		return nil, fmt.Errorf("platform: mute the output: OSStatus %d", int(st))
	}
	return func() error {
		if st := C.gsSetMute(dev, 0); st != 0 {
			return fmt.Errorf("platform: unmute the output: OSStatus %d", int(st))
		}
		return nil
	}, nil
}
