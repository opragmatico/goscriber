//go:build darwin

package platform

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>

static void goscriberSetDockIcon(int visible) {
	NSApplicationActivationPolicy policy = visible
		? NSApplicationActivationPolicyRegular
		: NSApplicationActivationPolicyAccessory;
	[[NSApplication sharedApplication] setActivationPolicy:policy];
}
*/
import "C"

// SetDockIcon shows or hides the app's Dock icon by switching the macOS
// activation policy between regular and accessory. An accessory app has no
// Dock icon and no menu bar of its own, but can still show windows.
//
// The Info.plist of a bundled app sets LSUIElement, so the app launches as an
// accessory. GLFW still makes every app a regular one when it initialises, and
// a plain binary (go run, make run) has no Info.plist, so call this once Fyne
// has started, on the main thread (from a lifecycle hook or fyne.Do).
//
// It does nothing on other systems.
func SetDockIcon(visible bool) {
	v := C.int(0)
	if visible {
		v = 1
	}
	C.goscriberSetDockIcon(v)
}
