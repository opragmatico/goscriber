//go:build darwin

package platform

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AVFoundation -framework ApplicationServices -framework Foundation
#import <AVFoundation/AVFoundation.h>
#import <ApplicationServices/ApplicationServices.h>
#import <Foundation/Foundation.h>

// gsMicrophoneStatus is the AVAuthorizationStatus for audio capture:
// NotDetermined 0, Restricted 1, Denied 2, Authorized 3.
static int gsMicrophoneStatus(void) {
	return (int)[AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
}

// gsRequestMicrophone asks for the microphone and waits for the answer: 1
// granted, 0 not. macOS shows its prompt only while the status is not
// determined; otherwise it answers at once. The answer arrives on another
// queue, so this must not run on a queue that answer needs.
static int gsRequestMicrophone(void) {
	__block BOOL granted = NO;
	dispatch_semaphore_t done = dispatch_semaphore_create(0);
	[AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(BOOL ok) {
		granted = ok;
		dispatch_semaphore_signal(done);
	}];
	dispatch_semaphore_wait(done, DISPATCH_TIME_FOREVER);
	dispatch_release(done);
	return granted ? 1 : 0;
}

// gsRequestAccessibility reports whether the process is trusted for
// Accessibility and, when it is not, has macOS offer to open System Settings.
static int gsRequestAccessibility(void) {
	int trusted = 0;
	@autoreleasepool {
		NSDictionary *opts = @{(id)kAXTrustedCheckOptionPrompt: @YES};
		trusted = AXIsProcessTrustedWithOptions((CFDictionaryRef)opts) ? 1 : 0;
	}
	return trusted;
}

static int gsListenEventAccess(void) { return CGPreflightListenEventAccess() ? 1 : 0; }
static int gsRequestListenEventAccess(void) { return CGRequestListenEventAccess() ? 1 : 0; }
*/
import "C"

// StatusOf returns where permission p stands, without asking for it.
//
// macOS reports Accessibility and Input Monitoring as granted or not, so for
// those "not granted" shows as StatusDenied even before the first prompt.
func StatusOf(p Permission) PermissionStatus {
	switch p {
	case PermMicrophone:
		return micStatus(int(C.gsMicrophoneStatus()))
	case PermAccessibility:
		return grantedOrDenied(AccessibilityTrusted())
	case PermInputMonitoring:
		return grantedOrDenied(C.gsListenEventAccess() != 0)
	default:
		return StatusNotNeeded
	}
}

// Request asks for permission p and returns where it stands afterwards.
//
//   - Microphone: macOS shows its prompt the first time and Request waits
//     for the user's answer, so call it off the main thread. Once answered,
//     it returns at once.
//   - Accessibility: macOS shows a dialog offering to open System Settings,
//     where the user turns it on; Request returns without waiting.
//   - Input Monitoring: likewise.
//
// For the packaged app the prompts name Goscriber; for a binary run from a
// terminal they name the terminal, which then holds the permission.
func Request(p Permission) PermissionStatus {
	switch p {
	case PermMicrophone:
		C.gsRequestMicrophone()
		return StatusOf(p)
	case PermAccessibility:
		return grantedOrDenied(C.gsRequestAccessibility() != 0)
	case PermInputMonitoring:
		return grantedOrDenied(C.gsRequestListenEventAccess() != 0)
	default:
		return StatusNotNeeded
	}
}

func grantedOrDenied(ok bool) PermissionStatus {
	if ok {
		return StatusGranted
	}
	return StatusDenied
}
