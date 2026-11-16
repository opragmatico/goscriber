//go:build linux

package x11test

/*
#cgo LDFLAGS: -lX11 -lXtst
#include <X11/Xlib.h>
#include <X11/extensions/XTest.h>
#include <X11/Xatom.h>
#include <X11/Xutil.h>
#include <X11/keysym.h>
#include <stdlib.h>
#include <string.h>
#include <sys/select.h>

// What this client copied to one selection, served while it owns it.
typedef struct {
	Atom sel;
	char* text;
} xtCopy;

typedef struct {
	Display* d;
	Window w;
	Atom clipboard, utf8, prop, targets;
	xtCopy copies[2]; // CLIPBOARD and PRIMARY
} xtClient;

static xtCopy* xtCopyOf(xtClient* c, Atom sel) {
	for (int i = 0; i < 2; i++) {
		if (c->copies[i].sel == sel) return &c->copies[i];
	}
	for (int i = 0; i < 2; i++) {
		if (c->copies[i].sel == None) {
			c->copies[i].sel = sel;
			return &c->copies[i];
		}
	}
	return &c->copies[0];
}

static xtClient* xtNew(int focus) {
	XInitThreads();
	Display* d = XOpenDisplay(NULL);
	if (d == NULL) return NULL;
	xtClient* c = calloc(1, sizeof(xtClient));
	c->d = d;
	c->w = XCreateSimpleWindow(d, DefaultRootWindow(d), 0, 0, 200, 100, 0, 0, 0);
	XSelectInput(d, c->w, KeyPressMask | KeyReleaseMask | StructureNotifyMask | PropertyChangeMask);
	c->clipboard = XInternAtom(d, "CLIPBOARD", False);
	c->utf8 = XInternAtom(d, "UTF8_STRING", False);
	c->prop = XInternAtom(d, "X11TEST_PASTE", False);
	c->targets = XInternAtom(d, "TARGETS", False);
	if (focus) {
		XMapRaised(d, c->w);
		XEvent ev;
		do { XNextEvent(d, &ev); } while (ev.type != MapNotify);
		XSetInputFocus(d, c->w, RevertToParent, CurrentTime);
	}
	XSync(d, False);
	return c;
}

static void xtFree(xtClient* c) {
	XDestroyWindow(c->d, c->w);
	XCloseDisplay(c->d);
	for (int i = 0; i < 2; i++) free(c->copies[i].text);
	free(c);
}

// xtOwn copies text to selection name, from this client.
static void xtOwn(xtClient* c, const char* name, const char* text) {
	Atom sel = XInternAtom(c->d, name, False);
	xtCopy* cp = xtCopyOf(c, sel);
	free(cp->text);
	cp->text = strdup(text);
	XSetSelectionOwner(c->d, sel, c->w, CurrentTime);
	XSync(c->d, False);
}

// Kinds of event xtNext reports.
enum { XT_NONE = 0, XT_KEY = 1, XT_PASTED = 2 };

// xtNext handles the events that arrive within ms. A key press other than
// a modifier is reported as XT_KEY with its keysym and modifier state; the
// paste keys (Ctrl+V, Shift+Insert, Ctrl+Shift+V) also ask for the
// clipboard, whose text arrives as XT_PASTED in *text (malloc'd). Requests
// for what this client copied are answered.
static int xtNext(xtClient* c, int ms, unsigned long* sym, unsigned int* state, char** text) {
	*text = NULL;
	while (1) {
		if (XPending(c->d) == 0) {
			int fd = ConnectionNumber(c->d);
			fd_set fds;
			FD_ZERO(&fds);
			FD_SET(fd, &fds);
			struct timeval tv = { ms / 1000, (ms % 1000) * 1000 };
			if (select(fd + 1, &fds, NULL, NULL, &tv) <= 0) return XT_NONE;
			if (XPending(c->d) == 0) return XT_NONE;
		}
		XEvent ev;
		XNextEvent(c->d, &ev);
		switch (ev.type) {
		case MappingNotify:
			XRefreshKeyboardMapping(&ev.xmapping);
			break;
		case KeyPress: {
			char buf[32];
			KeySym ks = NoSymbol;
			XLookupString(&ev.xkey, buf, sizeof buf, &ks, NULL);
			if (IsModifierKey(ks)) break;
			unsigned int mods = ev.xkey.state & (ShiftMask | ControlMask | Mod1Mask);
			if ((ks == XK_v || ks == XK_V) && (mods & ControlMask)) {
				XConvertSelection(c->d, c->clipboard, c->utf8, c->prop, c->w, CurrentTime);
			} else if (ks == XK_Insert && (mods & ShiftMask)) {
				XConvertSelection(c->d, c->clipboard, c->utf8, c->prop, c->w, CurrentTime);
			}
			*sym = ks;
			*state = mods;
			return XT_KEY;
		}
		case SelectionNotify: {
			if (ev.xselection.property == None) {
				*text = strdup("");
				return XT_PASTED;
			}
			Atom type;
			int format;
			unsigned long n, after;
			unsigned char* data = NULL;
			XGetWindowProperty(c->d, c->w, c->prop, 0, 1 << 20, True, AnyPropertyType, &type, &format, &n, &after, &data);
			*text = malloc(n + 1);
			memcpy(*text, data ? (char*)data : "", n);
			(*text)[n] = 0;
			if (data) XFree(data);
			return XT_PASTED;
		}
		case SelectionRequest: {
			XSelectionRequestEvent* r = &ev.xselectionrequest;
			XSelectionEvent reply;
			memset(&reply, 0, sizeof reply);
			reply.type = SelectionNotify;
			reply.requestor = r->requestor;
			reply.selection = r->selection;
			reply.target = r->target;
			reply.time = r->time;
			reply.property = None;
			xtCopy* cp = xtCopyOf(c, r->selection);
			if (cp->text != NULL) {
				if (r->target == c->targets) {
					Atom t[2] = { c->targets, c->utf8 };
					XChangeProperty(c->d, r->requestor, r->property, XA_ATOM, 32, PropModeReplace, (unsigned char*)t, 2);
					reply.property = r->property;
				} else if (r->target == c->utf8) {
					XChangeProperty(c->d, r->requestor, r->property, c->utf8, 8, PropModeReplace,
						(unsigned char*)cp->text, strlen(cp->text));
					reply.property = r->property;
				}
			}
			XSendEvent(c->d, r->requestor, False, 0, (XEvent*)&reply);
			XFlush(c->d);
			break;
		}
		case SelectionClear: {
			xtCopy* cp = xtCopyOf(c, ev.xselectionclear.selection);
			free(cp->text);
			cp->text = NULL;
			break;
		}
		}
	}
}

// xtKey presses (down) or lets go of the key that types sym, as the user
// holding a modifier.
static void xtKey(xtClient* c, unsigned long sym, int down) {
	XTestFakeKeyEvent(c->d, XKeysymToKeycode(c->d, sym), down, CurrentTime);
	XSync(c->d, False);
}

// xtTargets lists the targets the owner of selection name offers, as a
// malloc'd string of atom names separated by spaces, or NULL.
static char* xtTargets(xtClient* c, const char* name, int ms) {
	Atom sel = XInternAtom(c->d, name, False);
	XConvertSelection(c->d, sel, c->targets, c->prop, c->w, CurrentTime);
	XFlush(c->d);
	XEvent ev;
	for (int waited = 0; waited < ms; waited += 10) {
		if (XCheckTypedWindowEvent(c->d, c->w, SelectionNotify, &ev)) {
			if (ev.xselection.property == None) return NULL;
			Atom type;
			int format;
			unsigned long n, after;
			unsigned char* data = NULL;
			XGetWindowProperty(c->d, c->w, c->prop, 0, 1024, True, XA_ATOM, &type, &format, &n, &after, &data);
			size_t len = 1;
			char* out = calloc(1, 1);
			for (unsigned long i = 0; i < n; i++) {
				char* an = XGetAtomName(c->d, ((Atom*)data)[i]);
				out = realloc(out, len + strlen(an) + 1);
				strcat(out, an);
				strcat(out, " ");
				len += strlen(an) + 1;
				XFree(an);
			}
			if (data) XFree(data);
			return out;
		}
		struct timeval tv = { 0, 10000 };
		select(0, NULL, NULL, NULL, &tv);
	}
	return NULL;
}
*/
import "C"

import (
	"errors"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// X modifier masks and keysyms the tests look for.
const (
	ShiftMask   = C.ShiftMask
	ControlMask = C.ControlMask
	KeyReturn   = C.XK_Return
	KeyInsert   = C.XK_Insert
)

// Key is a key press the window got: its keysym and the modifiers held.
type Key struct {
	Sym   uint64
	State uint
}

// App is a client of the X server: a window that records what it gets,
// and copies text when asked. Its events are handled on a goroutine of its
// own, as a real app handles them while Goscriber types.
type App struct {
	c *C.xtClient

	mu     sync.Mutex
	typed  strings.Builder
	keys   []Key
	pasted []string
	ops    chan func(*C.xtClient)
	done   chan struct{}
}

// Start opens a client. With focus, its window is mapped and given the
// keyboard focus, so key events from XTEST land in it.
func Start(focus bool) (*App, error) {
	f := C.int(0)
	if focus {
		f = 1
	}
	c := C.xtNew(f)
	if c == nil {
		return nil, errors.New("x11test: cannot open the X display")
	}
	a := &App{c: c, ops: make(chan func(*C.xtClient)), done: make(chan struct{})}
	go a.loop()
	return a, nil
}

func (a *App) loop() {
	runtime.LockOSThread()
	defer close(a.done)
	for {
		select {
		case op, ok := <-a.ops:
			if !ok {
				C.xtFree(a.c)
				return
			}
			op(a.c)
			continue
		default:
		}
		var sym C.ulong
		var state C.uint
		var text *C.char
		switch C.xtNext(a.c, 10, &sym, &state, &text) {
		case C.XT_KEY:
			a.mu.Lock()
			a.keys = append(a.keys, Key{Sym: uint64(sym), State: uint(state)})
			if r, ok := keysymRune(uint64(sym)); ok {
				a.typed.WriteRune(r)
			}
			a.mu.Unlock()
		case C.XT_PASTED:
			a.mu.Lock()
			a.pasted = append(a.pasted, C.GoString(text))
			a.mu.Unlock()
			C.free(unsafe.Pointer(text))
		}
	}
}

// keysymRune is the character a keysym types: Latin-1 keysyms are their
// code points, Unicode keysyms carry theirs.
func keysymRune(sym uint64) (rune, bool) {
	switch {
	case sym == C.XK_Return:
		return '\n', true
	case sym == C.XK_Tab:
		return '\t', true
	case sym >= 0x20 && sym <= 0xff:
		return rune(sym), true
	case sym&0xff000000 == 0x01000000:
		return rune(sym & 0xffffff), true
	default:
		return 0, false
	}
}

func (a *App) do(op func(*C.xtClient)) {
	ran := make(chan struct{})
	a.ops <- func(c *C.xtClient) { op(c); close(ran) }
	<-ran
}

// Copy puts text on selection ("CLIPBOARD" or "PRIMARY") from this client,
// as the user copying in another app.
func (a *App) Copy(selection, text string) {
	a.do(func(c *C.xtClient) {
		cs, ct := C.CString(selection), C.CString(text)
		defer C.free(unsafe.Pointer(cs))
		defer C.free(unsafe.Pointer(ct))
		C.xtOwn(c, cs, ct)
	})
}

// Hold presses the key of keysym sym and keeps it down, as the user still
// holding the shortcut's modifiers; Release lets go of it.
func (a *App) Hold(sym uint64) { a.do(func(c *C.xtClient) { C.xtKey(c, C.ulong(sym), 1) }) }

// Release lets go of the key Hold pressed.
func (a *App) Release(sym uint64) { a.do(func(c *C.xtClient) { C.xtKey(c, C.ulong(sym), 0) }) }

// Targets lists the forms the owner of selection offers.
func (a *App) Targets(selection string) []string {
	var out []string
	a.do(func(c *C.xtClient) {
		cs := C.CString(selection)
		defer C.free(unsafe.Pointer(cs))
		if t := C.xtTargets(c, cs, 1000); t != nil {
			out = strings.Fields(C.GoString(t))
			C.free(unsafe.Pointer(t))
		}
	})
	return out
}

// Typed is the text typed into the window so far.
func (a *App) Typed() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.typed.String()
}

// Keys are the key presses so far, modifiers left out.
func (a *App) Keys() []Key {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Key(nil), a.keys...)
}

// Pasted are the texts the window pasted from the clipboard so far.
func (a *App) Pasted() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.pasted...)
}

// Wait polls cond until it holds or d passes, and reports whether it held.
func Wait(d time.Duration, cond func() bool) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

// Close closes the client.
func (a *App) Close() {
	close(a.ops)
	<-a.done
}
