//go:build linux

package platform

/*
#cgo LDFLAGS: -lX11 -lXtst
#include <X11/Xlib.h>
#include <X11/Xatom.h>
#include <X11/XKBlib.h>
#include <X11/keysym.h>
#include <X11/extensions/XTest.h>
#include <errno.h>
#include <limits.h>
#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/select.h>
#include <time.h>
#include <unistd.h>

// Errors these functions return.
enum {
	GSX_OK = 0,
	GSX_ENODISPLAY = -1, // no X server to talk to
	GSX_ENOXTEST = -2,   // the server has no XTEST extension
	GSX_ETIMEOUT = -3,   // the selection's owner did not answer in time
	GSX_ETOOBIG = -4,    // the selection is sent in pieces (INCR), which is not supported
	GSX_ENOKEY = -5,     // no key types the character, and none is free to map it to
};

// ---- Errors ----------------------------------------------------------------
//
// Xlib's default error handler ends the process. An app that requests the
// clipboard and closes its window before the answer turns Goscriber's
// answer into a BadWindow error, which must not end the app. The handler
// below swallows errors raised while a thread is inside these functions
// (gsxInCall), and passes the others to the handler it replaced, so errors
// on Fyne's own connection behave as before.

static __thread int gsxInCall;
static XErrorHandler gsxPrevHandler;
static int gsxLastError;

static int gsxErrorHandler(Display* d, XErrorEvent* e) {
	if (gsxInCall || gsxPrevHandler == NULL) {
		gsxLastError = e->error_code;
		return 0;
	}
	return gsxPrevHandler(d, e);
}

static pthread_once_t gsxInitOnce = PTHREAD_ONCE_INIT;

static void gsxInitThreads(void) {
	XInitThreads();
	gsxPrevHandler = XSetErrorHandler(gsxErrorHandler);
}

static void gsxInit(void) { pthread_once(&gsxInitOnce, gsxInitThreads); }

static Display* gsxOpen(void) {
	gsxInit();
	return XOpenDisplay(NULL);
}

static long gsxNowMs(void) {
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

// gsxWaitReadable waits up to ms for the connection to have data.
static int gsxWaitReadable(Display* d, long ms) {
	if (XPending(d) > 0) return 1;
	int fd = ConnectionNumber(d);
	fd_set fds;
	FD_ZERO(&fds);
	FD_SET(fd, &fds);
	struct timeval tv = { ms / 1000, (ms % 1000) * 1000 };
	return select(fd + 1, &fds, NULL, NULL, &tv) > 0;
}

// ---- Clipboard owner ---------------------------------------------------------
//
// X11 has no clipboard storage: the app that copied keeps the data and sends
// it to each app that pastes. Goscriber owns CLIPBOARD and PRIMARY from a
// hidden window on a connection of its own, and a thread (gsxClipServe, run
// by a locked goroutine) answers the requests. Under Wayland the same goes
// through XWayland, whose selections GNOME, KDE and wlroots compositors
// hand to Wayland apps.

enum { GSX_CLIPBOARD = 0, GSX_PRIMARY = 1, GSX_NSEL = 2 };

typedef struct {
	Display* d;
	Window w;
	Atom sel[GSX_NSEL];
	Atom targets, utf8, text, string, plainUTF8, plain, timestamp, secretHint;
	pthread_mutex_t mu;
	char* data[GSX_NSEL];
	size_t len[GSX_NSEL];
	int owned[GSX_NSEL];
	int transient[GSX_NSEL];
	Time since[GSX_NSEL];
	unsigned long changes; // CLIPBOARD's owner changed
	unsigned long reads;   // apps read CLIPBOARD from us
	size_t maxProperty;    // the largest selection one property can carry
} gsxClip;

static gsxClip* gsxClipNew(void) {
	Display* d = gsxOpen();
	if (d == NULL) return NULL;
	gsxClip* c = calloc(1, sizeof(gsxClip));
	c->d = d;
	c->w = XCreateSimpleWindow(d, DefaultRootWindow(d), 0, 0, 1, 1, 0, 0, 0);
	XSelectInput(d, c->w, PropertyChangeMask);
	c->sel[GSX_CLIPBOARD] = XInternAtom(d, "CLIPBOARD", False);
	c->sel[GSX_PRIMARY] = XA_PRIMARY;
	c->targets = XInternAtom(d, "TARGETS", False);
	c->utf8 = XInternAtom(d, "UTF8_STRING", False);
	c->text = XInternAtom(d, "TEXT", False);
	c->string = XA_STRING;
	c->plainUTF8 = XInternAtom(d, "text/plain;charset=utf-8", False);
	c->plain = XInternAtom(d, "text/plain", False);
	c->timestamp = XInternAtom(d, "TIMESTAMP", False);
	// KDE's marker for data a clipboard manager should not keep in its
	// history, the X11 counterpart of nspasteboard.org's transient type.
	c->secretHint = XInternAtom(d, "x-kde-passwordManagerHint", False);
	long max = XExtendedMaxRequestSize(d);
	if (max == 0) max = XMaxRequestSize(d);
	c->maxProperty = (size_t)max * 4 - 64;
	pthread_mutex_init(&c->mu, NULL);
	XFlush(d);
	return c;
}

static void gsxReply(gsxClip* c, XSelectionRequestEvent* r) {
	XSelectionEvent reply;
	memset(&reply, 0, sizeof(reply));
	reply.type = SelectionNotify;
	reply.display = r->display;
	reply.requestor = r->requestor;
	reply.selection = r->selection;
	reply.target = r->target;
	reply.time = r->time;
	reply.property = None;
	// Clients from before ICCCM 2 leave the property None.
	Atom prop = r->property != None ? r->property : r->target;

	int which = -1;
	for (int i = 0; i < GSX_NSEL; i++) {
		if (r->selection == c->sel[i]) which = i;
	}
	pthread_mutex_lock(&c->mu);
	if (which >= 0 && c->owned[which]) {
		if (r->target == c->targets) {
			Atom t[8];
			int n = 0;
			t[n++] = c->targets;
			t[n++] = c->timestamp;
			t[n++] = c->utf8;
			t[n++] = c->plainUTF8;
			t[n++] = c->string;
			t[n++] = c->text;
			if (c->transient[which]) t[n++] = c->secretHint;
			XChangeProperty(c->d, r->requestor, prop, XA_ATOM, 32, PropModeReplace, (unsigned char*)t, n);
			reply.property = prop;
		} else if (r->target == c->timestamp) {
			long t = (long)c->since[which];
			XChangeProperty(c->d, r->requestor, prop, XA_INTEGER, 32, PropModeReplace, (unsigned char*)&t, 1);
			reply.property = prop;
		} else if (r->target == c->secretHint && c->transient[which]) {
			XChangeProperty(c->d, r->requestor, prop, c->secretHint, 8, PropModeReplace, (unsigned char*)"secret", 6);
			reply.property = prop;
		} else if (r->target == c->utf8 || r->target == c->plainUTF8 || r->target == c->string ||
			r->target == c->text || r->target == c->plain) {
			// STRING is Latin-1 in theory; apps that ask for it get the
			// UTF-8 bytes, as most toolkits send them.
			Atom type = r->target == c->text ? c->utf8 : r->target;
			XChangeProperty(c->d, r->requestor, prop, type, 8, PropModeReplace,
				(unsigned char*)(c->data[which] ? c->data[which] : ""), (int)c->len[which]);
			reply.property = prop;
			if (which == GSX_CLIPBOARD) c->reads++;
		}
	}
	pthread_mutex_unlock(&c->mu);
	XSendEvent(c->d, r->requestor, False, NoEventMask, (XEvent*)&reply);
	XFlush(c->d);
}

// gsxClipServe answers the requests that arrive within ms.
static void gsxClipServe(gsxClip* c, long ms) {
	gsxInCall = 1;
	long until = gsxNowMs() + ms;
	for (;;) {
		while (XPending(c->d) > 0) {
			XEvent ev;
			XNextEvent(c->d, &ev);
			if (ev.type == SelectionRequest) {
				gsxReply(c, &ev.xselectionrequest);
			} else if (ev.type == SelectionClear) {
				pthread_mutex_lock(&c->mu);
				for (int i = 0; i < GSX_NSEL; i++) {
					if (ev.xselectionclear.selection == c->sel[i] && c->owned[i]) {
						c->owned[i] = 0;
						if (i == GSX_CLIPBOARD) c->changes++;
					}
				}
				pthread_mutex_unlock(&c->mu);
			}
		}
		long left = until - gsxNowMs();
		if (left <= 0 || !gsxWaitReadable(c->d, left)) break;
	}
	gsxInCall = 0;
}

// gsxClipSet takes the selections in mask (1 CLIPBOARD, 2 PRIMARY) with
// text, or gives them up when text is NULL. It returns 0, or -1 when
// another client kept the selection.
static int gsxClipSet(gsxClip* c, int mask, const char* text, size_t n, int transient) {
	gsxInCall = 1;
	int rc = 0;
	pthread_mutex_lock(&c->mu);
	for (int i = 0; i < GSX_NSEL; i++) {
		if (!(mask & (1 << i))) continue;
		free(c->data[i]);
		c->data[i] = NULL;
		c->len[i] = 0;
		if (text == NULL) {
			if (c->owned[i]) {
				XSetSelectionOwner(c->d, c->sel[i], None, CurrentTime);
				c->owned[i] = 0;
			}
			continue;
		}
		c->data[i] = malloc(n + 1);
		memcpy(c->data[i], text, n);
		c->data[i][n] = 0;
		c->len[i] = n;
		c->transient[i] = transient;
		c->since[i] = CurrentTime;
		XSetSelectionOwner(c->d, c->sel[i], c->w, CurrentTime);
		c->owned[i] = XGetSelectionOwner(c->d, c->sel[i]) == c->w;
		if (!c->owned[i]) rc = -1;
	}
	if (mask & 1) c->changes++;
	pthread_mutex_unlock(&c->mu);
	XFlush(c->d);
	gsxInCall = 0;
	return rc;
}

static void gsxClipCounts(gsxClip* c, unsigned long* changes, unsigned long* reads) {
	pthread_mutex_lock(&c->mu);
	*changes = c->changes;
	*reads = c->reads;
	pthread_mutex_unlock(&c->mu);
}

static int gsxClipOwns(gsxClip* c, int which) {
	pthread_mutex_lock(&c->mu);
	int owned = c->owned[which];
	pthread_mutex_unlock(&c->mu);
	return owned;
}

static size_t gsxClipMaxProperty(gsxClip* c) { return c->maxProperty; }

// gsxReadSelection reads the UTF-8 text of selection name ("CLIPBOARD" or
// "PRIMARY") through a connection of its own, waiting up to ms for the
// owner, which may be this process's own clipboard thread. It returns 1
// with *out (malloc'd) and *n set, 0 when the selection has no owner, 2
// when its owner offers no text (an image, files), or a GSX_E* error.
static int gsxReadSelection(const char* name, char** out, size_t* n, long ms) {
	*out = NULL;
	*n = 0;
	Display* d = gsxOpen();
	if (d == NULL) return GSX_ENODISPLAY;
	gsxInCall = 1;
	Atom sel = XInternAtom(d, name, False);
	int rc = 0;
	if (XGetSelectionOwner(d, sel) == None) goto done;
	Window w = XCreateSimpleWindow(d, DefaultRootWindow(d), 0, 0, 1, 1, 0, 0, 0);
	Atom prop = XInternAtom(d, "GOSCRIBER_SELECTION", False);
	Atom incr = XInternAtom(d, "INCR", False);
	Atom targets[2] = { XInternAtom(d, "UTF8_STRING", False), XA_STRING };
	rc = 2;
	for (int t = 0; t < 2 && rc == 2; t++) {
		XConvertSelection(d, sel, targets[t], prop, w, CurrentTime);
		XFlush(d);
		long until = gsxNowMs() + ms;
		XEvent ev;
		int got = 0;
		while (!got) {
			if (XCheckTypedWindowEvent(d, w, SelectionNotify, &ev)) {
				got = 1;
				break;
			}
			long left = until - gsxNowMs();
			if (left <= 0) break;
			gsxWaitReadable(d, left);
		}
		if (!got) {
			rc = GSX_ETIMEOUT;
			break;
		}
		if (ev.xselection.property == None) continue; // no such form: try the next
		Atom type;
		int format;
		unsigned long items, after;
		unsigned char* data = NULL;
		if (XGetWindowProperty(d, w, prop, 0, LONG_MAX / 4, True, AnyPropertyType,
			&type, &format, &items, &after, &data) != Success) {
			continue;
		}
		if (type == incr) {
			rc = GSX_ETOOBIG;
		} else if (format == 8) {
			*out = malloc(items + 1);
			memcpy(*out, data, items);
			(*out)[items] = 0;
			*n = items;
			rc = 1;
		}
		if (data != NULL) XFree(data);
	}
	XDestroyWindow(d, w);
done:
	XCloseDisplay(d);
	gsxInCall = 0;
	return rc;
}

// ---- Key events (XTEST) -----------------------------------------------------
//
// XTEST injects key events as if from the keyboard, into whatever window
// has the focus. Under Wayland they reach only X11 windows, so the typing
// tools take over there (internal/typing). Each call opens a connection of
// its own.

static int gsxXTestOpen(Display** out) {
	Display* d = gsxOpen();
	if (d == NULL) return GSX_ENODISPLAY;
	int ev, err, major, minor;
	if (!XTestQueryExtension(d, &ev, &err, &major, &minor)) {
		XCloseDisplay(d);
		return GSX_ENOXTEST;
	}
	*out = d;
	return GSX_OK;
}

static int gsxXTestAvailable(void) {
	Display* d = NULL;
	int rc = gsxXTestOpen(&d);
	if (rc == GSX_OK) XCloseDisplay(d);
	return rc;
}

// gsxReleaseHeld lets go of the modifier keys the user is still holding, so
// the Ctrl+Alt of Ctrl+Alt+Space does not turn the paste keys into
// another shortcut.
static void gsxReleaseHeld(Display* d) {
	char keys[32];
	XQueryKeymap(d, keys);
	XModifierKeymap* mm = XGetModifierMapping(d);
	if (mm == NULL) return;
	for (int i = 0; i < 8 * mm->max_keypermod; i++) {
		KeyCode kc = mm->modifiermap[i];
		if (kc != 0 && (keys[kc / 8] & (1 << (kc % 8)))) {
			XTestFakeKeyEvent(d, kc, False, CurrentTime);
		}
	}
	XFreeModifiermap(mm);
	XSync(d, False);
}

static void gsxTap(Display* d, KeyCode kc) {
	XTestFakeKeyEvent(d, kc, True, CurrentTime);
	XTestFakeKeyEvent(d, kc, False, CurrentTime);
}

// gsxKeys presses the n keys in syms in order, then lets go of them in
// reverse: a chord such as Control_L, v.
static int gsxKeys(const unsigned long* syms, int n) {
	Display* d = NULL;
	int rc = gsxXTestOpen(&d);
	if (rc != GSX_OK) return rc;
	gsxInCall = 1;
	KeyCode codes[8];
	for (int i = 0; i < n && i < 8; i++) {
		codes[i] = XKeysymToKeycode(d, (KeySym)syms[i]);
		if (codes[i] == 0) {
			rc = GSX_ENOKEY;
			goto done;
		}
	}
	gsxReleaseHeld(d);
	for (int i = 0; i < n; i++) XTestFakeKeyEvent(d, codes[i], True, CurrentTime);
	for (int i = n - 1; i >= 0; i--) XTestFakeKeyEvent(d, codes[i], False, CurrentTime);
	XSync(d, False);
done:
	XCloseDisplay(d);
	gsxInCall = 0;
	return rc;
}

static KeySym gsxKeysym(uint32_t cp) {
	switch (cp) {
	case '\n':
	case '\r':
		return XK_Return;
	case '\t':
		return XK_Tab;
	}
	if ((cp >= 0x20 && cp <= 0x7e) || (cp >= 0xa0 && cp <= 0xff)) return cp; // Latin-1 keysyms are the code points
	return 0x01000000 | cp;
}

// gsxType types the n code points in cps. A character on a key of the
// current layout (either level) is typed with that key; any other is
// mapped onto a spare key code for the moment, as xdotool does. Caps Lock
// is turned off while typing and put back after.
static int gsxType(const uint32_t* cps, int n, int keyDelayUs, int remapDelayUs) {
	Display* d = NULL;
	int rc = gsxXTestOpen(&d);
	if (rc != GSX_OK) return rc;
	gsxInCall = 1;

	XkbStateRec st;
	XkbGetState(d, XkbUseCoreKbd, &st);
	int group = st.group;
	int caps = st.locked_mods & LockMask;
	if (caps) XkbLockModifiers(d, XkbUseCoreKbd, LockMask, 0);
	gsxReleaseHeld(d);

	int minKc, maxKc, per;
	XDisplayKeycodes(d, &minKc, &maxKc);
	KeySym* map = XGetKeyboardMapping(d, minKc, maxKc - minKc + 1, &per);
	KeyCode spare = 0;
	for (int kc = maxKc; kc >= minKc && spare == 0 && map != NULL; kc--) {
		int empty = 1;
		for (int i = 0; i < per; i++) {
			if (map[(kc - minKc) * per + i] != NoSymbol) empty = 0;
		}
		if (empty) spare = kc;
	}
	if (map != NULL) XFree(map);
	KeyCode shift = XKeysymToKeycode(d, XK_Shift_L);
	KeySym onSpare = NoSymbol;

	for (int i = 0; i < n; i++) {
		KeySym ks = gsxKeysym(cps[i]);
		KeyCode kc = XKeysymToKeycode(d, ks);
		int level = -1;
		if (kc != 0) {
			if (XkbKeycodeToKeysym(d, kc, group, 0) == ks) {
				level = 0;
			} else if (shift != 0 && XkbKeycodeToKeysym(d, kc, group, 1) == ks) {
				level = 1;
			}
		}
		if (level == 0) {
			gsxTap(d, kc);
		} else if (level == 1) {
			XTestFakeKeyEvent(d, shift, True, CurrentTime);
			gsxTap(d, kc);
			XTestFakeKeyEvent(d, shift, False, CurrentTime);
		} else if (spare != 0) {
			if (onSpare != ks) {
				// Apps read the new mapping when they handle the
				// MappingNotify, so give them time before the key
				// press, and before the next change.
				KeySym syms[2] = { ks, ks };
				XChangeKeyboardMapping(d, spare, 2, syms, 1);
				XSync(d, False);
				onSpare = ks;
				usleep(remapDelayUs);
			}
			gsxTap(d, spare);
			XSync(d, False);
			usleep(remapDelayUs);
		} else {
			rc = GSX_ENOKEY;
			break;
		}
		XSync(d, False);
		if (keyDelayUs > 0) usleep(keyDelayUs);
	}
	if (onSpare != NoSymbol) {
		KeySym none[2] = { NoSymbol, NoSymbol };
		XChangeKeyboardMapping(d, spare, 2, none, 1);
	}
	if (caps) XkbLockModifiers(d, XkbUseCoreKbd, LockMask, LockMask);
	XSync(d, False);
	XCloseDisplay(d);
	gsxInCall = 0;
	return rc;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/opragmatico/goscriber/internal/config"
)

// errNoDisplay means there is no X server to talk to: DISPLAY is not set,
// or Wayland runs without XWayland.
var errNoDisplay = errors.New("cannot open the X11 display (is DISPLAY set? under Wayland, Goscriber needs XWayland)")

// x11Error turns a GSX_E* code into an error.
func x11Error(rc C.int) error {
	switch rc {
	case C.GSX_OK:
		return nil
	case C.GSX_ENODISPLAY:
		return errNoDisplay
	case C.GSX_ENOXTEST:
		return errors.New("the X server has no XTEST extension, so Goscriber cannot press keys; choose xdotool as the typing tool")
	case C.GSX_ETIMEOUT:
		return errors.New("the app that holds the clipboard did not answer")
	case C.GSX_ETOOBIG:
		return errors.New("the clipboard holds too much text to save")
	case C.GSX_ENOKEY:
		return errors.New("no key of the keyboard layout types a character, and no key is free to map it to")
	default:
		return fmt.Errorf("X11 error %d", int(rc))
	}
}

// ---- Clipboard ----------------------------------------------------------------

const (
	// selectionTimeout bounds a read of the clipboard: an app that does not
	// answer by then is not going to.
	selectionTimeout = time.Second
	// servePoll is how long the clipboard thread waits for requests before
	// it checks whether it should stop; it answers each request as it
	// arrives.
	servePoll = 250 * time.Millisecond
)

// x11Clipboard owns CLIPBOARD and PRIMARY. One serves the process: an X11
// selection lives as long as the window that owns it.
type x11Clipboard struct {
	c *C.gsxClip
}

var (
	clipOnce sync.Once
	clip     *x11Clipboard
	clipErr  error
)

// theClipboard starts the clipboard thread the first time it is needed.
func theClipboard() (*x11Clipboard, error) {
	clipOnce.Do(func() {
		c := C.gsxClipNew()
		if c == nil {
			clipErr = errNoDisplay
			return
		}
		clip = &x11Clipboard{c: c}
		started := make(chan struct{})
		go func() {
			// The thread answers on the connection the window belongs to,
			// for as long as the process runs.
			runtime.LockOSThread()
			close(started)
			for {
				C.gsxClipServe(c, C.long(servePoll.Milliseconds()))
			}
		}()
		<-started
	})
	return clip, clipErr
}

// selection masks for gsxClipSet.
const (
	maskClipboard = 1 << C.GSX_CLIPBOARD
	maskPrimary   = 1 << C.GSX_PRIMARY
)

func (x *x11Clipboard) set(mask int, text string, transient bool) error {
	if text == "" {
		// An empty selection is no selection.
		C.gsxClipSet(x.c, C.int(mask), nil, 0, 0)
		return nil
	}
	cs := C.CString(text)
	defer C.free(unsafe.Pointer(cs))
	t := C.int(0)
	if transient {
		t = 1
	}
	if C.gsxClipSet(x.c, C.int(mask), cs, C.size_t(len(text)), t) != 0 {
		return errors.New("another app kept the clipboard")
	}
	return nil
}

func (x *x11Clipboard) clear(mask int) { C.gsxClipSet(x.c, C.int(mask), nil, 0, 0) }

func (x *x11Clipboard) counts() (changes, reads uint64) {
	var ch, rd C.ulong
	C.gsxClipCounts(x.c, &ch, &rd)
	return uint64(ch), uint64(rd)
}

// ownsPrimary reports whether Goscriber still holds PRIMARY.
func (x *x11Clipboard) ownsPrimary() bool { return C.gsxClipOwns(x.c, C.GSX_PRIMARY) != 0 }

func (x *x11Clipboard) maxText() int { return int(C.gsxClipMaxProperty(x.c)) }

// selection is what a selection holds.
type selection int

const (
	selEmpty selection = iota // no app holds it
	selText                   // text
	selOther                  // something that is not text: an image, files
)

// readSelection reads the text of selection name ("CLIPBOARD" or
// "PRIMARY").
func readSelection(name string) (string, selection, error) {
	cn := C.CString(name)
	defer C.free(unsafe.Pointer(cn))
	var out *C.char
	var n C.size_t
	switch rc := C.gsxReadSelection(cn, &out, &n, C.long(selectionTimeout.Milliseconds())); rc {
	case 0:
		return "", selEmpty, nil
	case 1:
		defer C.free(unsafe.Pointer(out))
		return C.GoStringN(out, C.int(n)), selText, nil
	case 2:
		return "", selOther, nil
	default:
		return "", selEmpty, x11Error(rc)
	}
}

// ---- Key events -----------------------------------------------------------------

// X11 keysyms (X11/keysymdef.h).
const (
	xkShiftL   = 0xffe1
	xkControlL = 0xffe3
	xkInsert   = 0xff63
	xkReturn   = 0xff0d
	xkV        = 0x0076
)

// Pauses between typed keys, and around a key mapped for a character the
// layout lacks.
const (
	xKeyDelay   = 2 * time.Millisecond
	xRemapDelay = 25 * time.Millisecond
)

// xtestAvailable reports whether Goscriber can press keys with XTEST.
func xtestAvailable() error { return x11Error(C.gsxXTestAvailable()) }

func xtestKeys(syms ...C.ulong) error {
	return x11Error(C.gsxKeys(&syms[0], C.int(len(syms))))
}

// xtest presses keys with X11 key events. It implements keyPresser.
type xtest struct{}

func (xtest) Paste(keys config.PasteKeys) error {
	switch keys {
	case config.PasteShiftInsert:
		return xtestKeys(xkShiftL, xkInsert)
	case config.PasteCtrlShiftV:
		return xtestKeys(xkControlL, xkShiftL, xkV)
	default:
		return xtestKeys(xkControlL, xkV)
	}
}

func (xtest) PressReturn() error { return xtestKeys(xkReturn) }

func (xtest) Type(text string) error {
	cps := make([]C.uint32_t, 0, len(text))
	for _, r := range text {
		cps = append(cps, C.uint32_t(r))
	}
	if len(cps) == 0 {
		return nil
	}
	return x11Error(C.gsxType(&cps[0], C.int(len(cps)),
		C.int(xKeyDelay.Microseconds()), C.int(xRemapDelay.Microseconds())))
}
