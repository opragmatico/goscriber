//go:build darwin

package platform

/*
#cgo LDFLAGS: -framework ApplicationServices -framework Carbon -framework Foundation
#import <Foundation/Foundation.h>
#import <ApplicationServices/ApplicationServices.h>
#import <Carbon/Carbon.h>
#include <unistd.h>

static int gsAccessibilityTrusted(void) { return AXIsProcessTrusted() ? 1 : 0; }

// gsResolveCommandV finds the key that types "v" with Command held in the
// current keyboard layout: kVK_ANSI_V on QWERTY, another key on Dvorak.
// Non-Latin layouts usually map Command shortcuts to QWERTY positions, which
// asking with Command held accounts for. It must run on the main thread.
static int gsResolveCommandV(void) {
	int keycode = -1;
	TISInputSourceRef src = TISCopyCurrentKeyboardLayoutInputSource();
	if (src == NULL) {
		return -1;
	}
	CFDataRef data = (CFDataRef)TISGetInputSourceProperty(src, kTISPropertyUnicodeKeyLayoutData);
	if (data != NULL) {
		const UCKeyboardLayout* layout = (const UCKeyboardLayout*)CFDataGetBytePtr(data);
		UInt32 kbType = LMGetKbdType();
		for (UInt16 kc = 0; kc < 128 && keycode < 0; kc++) {
			UInt32 dead = 0;
			UniChar chars[4];
			UniCharCount n = 0;
			OSStatus s = UCKeyTranslate(layout, kc, kUCKeyActionDisplay, (cmdKey >> 8) & 0xFF, kbType,
				kUCKeyTranslateNoDeadKeysMask, &dead, 4, &n, chars);
			if (s == noErr && n == 1 && chars[0] == 'v') {
				keycode = kc;
			}
		}
	}
	CFRelease(src);
	return keycode;
}

// gsCommandVKeycode runs gsResolveCommandV on the main thread and waits up
// to timeoutMs for it: -1 when the layout has no such key, -2 when the main
// thread did not answer in time (no Cocoa event loop is running).
static int gsCommandVKeycode(long timeoutMs) {
	if ([NSThread isMainThread]) {
		return gsResolveCommandV();
	}
	__block int result = -1;
	dispatch_semaphore_t done = dispatch_semaphore_create(0);
	dispatch_async(dispatch_get_main_queue(), ^{
		result = gsResolveCommandV();
		dispatch_semaphore_signal(done);
	});
	long late = dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, timeoutMs * NSEC_PER_MSEC));
	int r = late ? -2 : result;
	dispatch_release(done);
	return r;
}

// Events come from a private source and carry explicit flags, so modifiers
// the user is still holding (the Option of Option+Space) do not leak into
// them: Option+Command+V is "paste and match style", or a move in Finder.
static CGEventSourceRef gsSource(void) {
	return CGEventSourceCreate(kCGEventSourceStatePrivate);
}

static void gsPostKey(CGEventSourceRef src, CGKeyCode key, bool down, CGEventFlags flags) {
	CGEventRef e = CGEventCreateKeyboardEvent(src, key, down);
	CGEventSetFlags(e, flags);
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
}

// gsPostPaste presses Command, clicks the V key, holds Command for holdMs
// (some apps read the modifier state when they handle the key) and lets go.
static void gsPostPaste(int vKey, int holdMs) {
	CGEventSourceRef src = gsSource();
	gsPostKey(src, kVK_Command, true, kCGEventFlagMaskCommand);
	gsPostKey(src, (CGKeyCode)vKey, true, kCGEventFlagMaskCommand);
	gsPostKey(src, (CGKeyCode)vKey, false, kCGEventFlagMaskCommand);
	usleep(holdMs * 1000);
	gsPostKey(src, kVK_Command, false, 0);
	if (src != NULL) CFRelease(src);
}

static void gsPostReturn(void) {
	CGEventSourceRef src = gsSource();
	gsPostKey(src, kVK_Return, true, 0);
	gsPostKey(src, kVK_Return, false, 0);
	if (src != NULL) CFRelease(src);
}

// gsPostUnicode types n UTF-16 units (at most 20, the most one event
// carries) as one key press.
static void gsPostUnicode(const UniChar* s, int n) {
	CGEventSourceRef src = gsSource();
	CGEventRef down = CGEventCreateKeyboardEvent(src, 0, true);
	CGEventSetFlags(down, 0);
	CGEventKeyboardSetUnicodeString(down, n, s);
	CGEventPost(kCGHIDEventTap, down);
	CFRelease(down);
	CGEventRef up = CGEventCreateKeyboardEvent(src, 0, false);
	CGEventSetFlags(up, 0);
	CGEventKeyboardSetUnicodeString(up, n, s);
	CGEventPost(kCGHIDEventTap, up);
	CFRelease(up);
	if (src != NULL) CFRelease(src);
}
*/
import "C"

import (
	"fmt"
	"log/slog"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/opragmatico/goscriber/internal/output"
)

const (
	// ansiV is kVK_ANSI_V, the V key of a US keyboard, used when the
	// layout cannot be read.
	ansiV = 9
	// pasteHold is how long Command stays down after the V click, as in
	// Handy.
	pasteHold = 100 * time.Millisecond
	// layoutTimeout bounds the wait for the main thread to read the
	// keyboard layout.
	layoutTimeout = 500 * time.Millisecond
	// maxUnicodeUnits is the most UTF-16 units one keyboard event carries.
	maxUnicodeUnits = 20
	// typePause is the pause between typed chunks, so apps keep up.
	typePause = 2 * time.Millisecond
)

// Keyboard posts synthetic key presses to the focused app through Core
// Graphics. It implements output.Keyboard. Posting needs the Accessibility
// permission: without it macOS drops the events silently, so every method
// checks first and fails with an error matching output.ErrNoPermission.
type Keyboard struct {
	// Log receives a warning when the keyboard layout cannot be read. Nil
	// means slog's default logger.
	Log *slog.Logger
}

var _ output.Keyboard = Keyboard{}

// AccessibilityTrusted reports whether Goscriber has the Accessibility
// permission, which posting key presses to other apps (and the shortcuts'
// event tap) needs.
func AccessibilityTrusted() bool { return C.gsAccessibilityTrusted() != 0 }

func checkTrusted() error {
	if !AccessibilityTrusted() {
		return fmt.Errorf("%w: grant Goscriber the Accessibility permission in System Settings → Privacy & Security", output.ErrNoPermission)
	}
	return nil
}

func (k Keyboard) log() *slog.Logger {
	if k.Log != nil {
		return k.Log
	}
	return slog.Default()
}

// Paste implements output.Keyboard: Command and the key that types "v" in
// the active layout. The layout is read on the main thread, which needs
// Cocoa's event loop running there; without it the US V key is used.
func (k Keyboard) Paste() error {
	if err := checkTrusted(); err != nil {
		return err
	}
	key := int(C.gsCommandVKeycode(C.long(layoutTimeout.Milliseconds())))
	if key < 0 {
		k.log().Warn("could not find the V key of the keyboard layout; pasting with the US V key", "reason", key)
		key = ansiV
	}
	C.gsPostPaste(C.int(key), C.int(pasteHold.Milliseconds()))
	return nil
}

// PasteKeyName describes the paste shortcut in the active keyboard layout,
// such as "Cmd+V (key code 9)", for diagnostics.
func PasteKeyName() string {
	switch key := int(C.gsCommandVKeycode(C.long(layoutTimeout.Milliseconds()))); key {
	case -1:
		return fmt.Sprintf("Cmd+V (no V in this layout; key code %d)", ansiV)
	case -2:
		return fmt.Sprintf("Cmd+V (layout unreadable off the main thread; key code %d)", ansiV)
	default:
		return fmt.Sprintf("Cmd+V (key code %d)", key)
	}
}

// PressReturn implements output.Keyboard.
func (k Keyboard) PressReturn() error {
	if err := checkTrusted(); err != nil {
		return err
	}
	C.gsPostReturn()
	return nil
}

// Type implements output.Keyboard: the text goes out as Unicode strings
// attached to key events, which works for any character in any layout.
func (k Keyboard) Type(text string) error {
	if err := checkTrusted(); err != nil {
		return err
	}
	for i, chunk := range utf16Chunks(text, maxUnicodeUnits) {
		if i > 0 {
			time.Sleep(typePause)
		}
		C.gsPostUnicode((*C.UniChar)(unsafe.Pointer(&chunk[0])), C.int(len(chunk)))
	}
	return nil
}

// utf16Chunks splits text into UTF-16 chunks of at most n units, never
// between the two halves of a surrogate pair.
func utf16Chunks(text string, n int) [][]uint16 {
	units := utf16.Encode([]rune(text))
	var chunks [][]uint16
	for len(units) > 0 {
		k := min(n, len(units))
		if k < len(units) && utf16.IsSurrogate(rune(units[k-1])) && units[k-1] < 0xDC00 {
			k-- // a high surrogate: keep it with its low half
		}
		chunks = append(chunks, units[:k])
		units = units[k:]
	}
	return chunks
}
