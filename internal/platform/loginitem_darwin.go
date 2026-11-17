//go:build darwin

package platform

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit -framework ServiceManagement
#import <AppKit/AppKit.h>
#import <ServiceManagement/ServiceManagement.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>

// gsLoginItemStatus is the state of the app's SMAppService.mainApp login
// item.
enum {
	gsLoginOldMacOS = -2,     // before macOS 13, which has no SMAppService
	gsLoginNoBundle = -1,     // a bare executable, which has no login item
	gsLoginOff = 0,
	gsLoginOn = 1,
	gsLoginNeedsApproval = 2, // registered; waits for the user in System Settings
};

// A bare executable has no Info.plist, so no bundle identifier. An app
// bundle that was never registered reads as SMAppServiceStatusNotFound
// rather than NotRegistered (macOS 15), so both mean off.
static int gsLoginItemStatus(void) {
	if (@available(macOS 13.0, *)) {
		@autoreleasepool {
			if ([[NSBundle mainBundle] bundleIdentifier] == nil) {
				return gsLoginNoBundle;
			}
			switch ([[SMAppService mainAppService] status]) {
			case SMAppServiceStatusEnabled:
				return gsLoginOn;
			case SMAppServiceStatusRequiresApproval:
				return gsLoginNeedsApproval;
			default:
				return gsLoginOff;
			}
		}
	}
	return gsLoginOldMacOS;
}

// gsSetLoginItem registers or unregisters the login item. It returns NULL,
// or the error's description, which the caller frees.
static char *gsSetLoginItem(int on) {
	if (@available(macOS 13.0, *)) {
		@autoreleasepool {
			NSError *err = nil;
			SMAppService *app = [SMAppService mainAppService];
			BOOL ok = on ? [app registerAndReturnError:&err] : [app unregisterAndReturnError:&err];
			if (ok) {
				return NULL;
			}
			NSString *msg = err ? [NSString stringWithFormat:@"%@ (%@ %ld)", err.localizedDescription, err.domain, (long)err.code]
			                    : @"unknown error";
			return strdup(msg.UTF8String);
		}
	}
	return strdup("SMAppService needs macOS 13");
}

static atomic_int gsAtLogin;

// gsWatchLaunch notes whether macOS opened the app as a login item. The
// open-application Apple Event says so (keyAELaunchedAsLogInItem), and it is
// the current event only while AppKit finishes launching, so an observer of
// NSApplicationDidFinishLaunchingNotification reads it. GLFW runs that
// launch when Fyne initialises it; the observer must be in place before.
static void gsWatchLaunch(void) {
	static dispatch_once_t once;
	dispatch_once(&once, ^{
		@autoreleasepool {
			[[NSNotificationCenter defaultCenter]
				addObserverForName:NSApplicationDidFinishLaunchingNotification
				            object:nil
				             queue:nil
				        usingBlock:^(NSNotification *note) {
					NSAppleEventDescriptor *ev = [[NSAppleEventManager sharedAppleEventManager] currentAppleEvent];
					if ([ev eventClass] == kCoreEventClass && [ev eventID] == kAEOpenApplication &&
					    [[ev paramDescriptorForKeyword:keyAEPropData] enumCodeValue] == keyAELaunchedAsLogInItem) {
						atomic_store(&gsAtLogin, 1);
					}
				}];
		}
	});
}

static int gsLaunchedAtLogin(void) { return atomic_load(&gsAtLogin); }
*/
import "C"

import (
	"fmt"
	"unsafe"
)

var (
	errLoginOldMacOS = unsupportedError("launching at login needs macOS 13 or later")
	errLoginNoBundle = unsupportedError("only Goscriber.app can open at login, not a bare executable")
)

// loginItemStatus reads the login item's state, or why there is none.
func loginItemStatus() (C.int, error) {
	switch st := C.gsLoginItemStatus(); st {
	case C.gsLoginOldMacOS:
		return st, errLoginOldMacOS
	case C.gsLoginNoBundle:
		return st, errLoginNoBundle
	default:
		return st, nil
	}
}

// LoginItemSupported reports whether Goscriber can launch at login: from
// its app bundle, on macOS 13 or later.
func LoginItemSupported() error {
	_, err := loginItemStatus()
	return err
}

// LoginItemEnabled reports whether macOS opens Goscriber when the user logs
// in. A login item waiting for the user's approval does not count.
func LoginItemEnabled() (bool, error) {
	st, err := loginItemStatus()
	return st == C.gsLoginOn, err
}

// SetLoginItem registers the app bundle as a login item, or unregisters it.
// Registering makes macOS show a "Login Item Added" notification. When the
// user has to allow the login item in System Settings first, it returns
// ErrLoginItemNeedsApproval.
func SetLoginItem(on bool) error {
	st, err := loginItemStatus()
	switch {
	case err != nil:
		return err
	case on && st == C.gsLoginOn, !on && st == C.gsLoginOff:
		return nil
	case on && st == C.gsLoginNeedsApproval:
		return ErrLoginItemNeedsApproval
	}
	v := C.int(0)
	if on {
		v = 1
	}
	if msg := C.gsSetLoginItem(v); msg != nil {
		defer C.free(unsafe.Pointer(msg))
		verb := "register"
		if !on {
			verb = "unregister"
		}
		return fmt.Errorf("platform: %s the login item: %s", verb, C.GoString(msg))
	}
	if on {
		if st, _ := loginItemStatus(); st == C.gsLoginNeedsApproval {
			return ErrLoginItemNeedsApproval
		}
	}
	return nil
}

// WatchLaunch starts watching for macOS to finish launching the app, so
// LaunchedAtLogin can tell a launch at login. Call it before the Fyne app
// starts; later calls do nothing.
func WatchLaunch() { C.gsWatchLaunch() }

// LaunchedAtLogin reports whether macOS opened Goscriber as a login item.
// It is known once the app has finished launching, from Fyne's OnStarted
// on, and only after WatchLaunch.
func LaunchedAtLogin() bool { return C.gsLaunchedAtLogin() != 0 }
