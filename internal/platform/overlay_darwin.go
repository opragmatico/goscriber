//go:build darwin

package platform

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit -framework QuartzCore
#import <AppKit/AppKit.h>
#import <QuartzCore/QuartzCore.h>
#include <stdint.h>

// gsOverlayPrepare turns a borderless window into the recording overlay: it
// floats above other windows, full-screen apps included, appears on every
// Space, stays out of the window cycle, casts no shadow, has its content
// clipped to a pill, and lets clicks through until gsOverlayClickThrough says
// otherwise.
static void gsOverlayPrepare(uintptr_t ptr, double radius) {
	NSWindow *win = (NSWindow *)ptr;
	[win setLevel:NSStatusWindowLevel];
	[win setCollectionBehavior:NSWindowCollectionBehaviorCanJoinAllSpaces |
		NSWindowCollectionBehaviorStationary |
		NSWindowCollectionBehaviorFullScreenAuxiliary |
		NSWindowCollectionBehaviorIgnoresCycle];
	[win setHidesOnDeactivate:NO];
	[win setHasShadow:NO];
	[win setOpaque:NO];
	[win setBackgroundColor:[NSColor clearColor]];
	[win setIgnoresMouseEvents:YES];
	NSView *view = [win contentView];
	[view setWantsLayer:YES];
	[[view layer] setCornerRadius:radius];
	[[view layer] setMasksToBounds:YES];
}

// gsOverlayPlace centres the window horizontally on the screen that holds
// the mouse pointer, margin points from the top or the bottom of the area
// the menu bar and the Dock leave free.
static void gsOverlayPlace(uintptr_t ptr, int top, double margin) {
	NSWindow *win = (NSWindow *)ptr;
	NSPoint mouse = [NSEvent mouseLocation];
	NSScreen *screen = [NSScreen mainScreen];
	for (NSScreen *s in [NSScreen screens]) {
		if (NSMouseInRect(mouse, [s frame], NO)) {
			screen = s;
			break;
		}
	}
	if (screen == nil) {
		return;
	}
	NSRect area = [screen visibleFrame];
	NSSize size = [win frame].size;
	CGFloat x = NSMidX(area) - size.width / 2;
	CGFloat y = top ? NSMaxY(area) - size.height - margin : NSMinY(area) + margin;
	[win setFrameOrigin:NSMakePoint(round(x), round(y))];
}

// gsOverlayClickThrough lets clicks through the window, to whatever is
// under it, except while the mouse is over the rectangle x, y, w, h (in
// points from the window's top left corner).
static void gsOverlayClickThrough(uintptr_t ptr, double x, double y, double w, double h) {
	NSWindow *win = (NSWindow *)ptr;
	NSRect frame = [win frame];
	NSRect hot = NSMakeRect(NSMinX(frame) + x, NSMaxY(frame) - y - h, w, h);
	BOOL over = NSMouseInRect([NSEvent mouseLocation], hot, NO);
	if ([win ignoresMouseEvents] == over) {
		[win setIgnoresMouseEvents:!over];
	}
}

static void gsYieldFocus(void) {
	if ([NSApp isActive]) {
		[NSApp deactivate];
	}
}
*/
import "C"

import "fyne.io/fyne/v2/driver"

// PrepareOverlay makes the window behind native, the context Fyne's
// driver.NativeWindow.RunNative passes, the recording overlay: floating
// above other windows and full-screen apps, on every Space, out of the
// window cycle, shadowless, clipped to a pill with corners of radius points,
// and click-through until OverlayClickThrough says otherwise. Call it on the
// main thread once the window exists. Elsewhere than macOS it does nothing.
func PrepareOverlay(native any, radius float32) {
	if ctx, ok := native.(driver.MacWindowContext); ok && ctx.NSWindow != 0 {
		C.gsOverlayPrepare(C.uintptr_t(ctx.NSWindow), C.double(radius))
	}
}

// PlaceOverlay centres the window horizontally on the screen holding the
// mouse pointer, margin points from the top or bottom of the screen's free
// area. Call it on the main thread after the window is shown or resized.
func PlaceOverlay(native any, top bool, margin float32) {
	if ctx, ok := native.(driver.MacWindowContext); ok && ctx.NSWindow != 0 {
		t := C.int(0)
		if top {
			t = 1
		}
		C.gsOverlayPlace(C.uintptr_t(ctx.NSWindow), t, C.double(margin))
	}
}

// OverlayClickThrough lets clicks through the overlay except over the
// rectangle at x, y of size w, h, in points from the window's top left: the
// cancel button. AppKit can only take or pass a window's clicks as a whole,
// so call it on the main thread several times a second while the overlay is
// shown; it follows the mouse pointer.
func OverlayClickThrough(native any, x, y, w, h float32) {
	if ctx, ok := native.(driver.MacWindowContext); ok && ctx.NSWindow != 0 {
		C.gsOverlayClickThrough(C.uintptr_t(ctx.NSWindow), C.double(x), C.double(y), C.double(w), C.double(h))
	}
}

// YieldFocus hands the keyboard back to the app that had it, after a click
// on the overlay made Goscriber the active app. Call it on the main thread.
func YieldFocus() { C.gsYieldFocus() }
