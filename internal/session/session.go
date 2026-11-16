// Package session tells what kind of graphical session Goscriber runs in on
// Linux: X11 or Wayland. Global shortcuts and synthetic key presses work
// differently in each: an X11 client can grab keys and send key events to
// every other window, while Wayland lets no app do either, and X11 clients
// running through XWayland reach only the other X11 windows.
package session

import (
	"os"
	"runtime"
	"strings"
)

// Kind is a kind of session.
type Kind int

// Kinds of session.
const (
	// None is no graphical session Goscriber knows how to reach: neither
	// DISPLAY nor WAYLAND_DISPLAY is set.
	None Kind = iota
	// X11 is an X11 session.
	X11
	// Wayland is a Wayland session. Goscriber's windows, its clipboard and
	// the built-in key events still go through XWayland when DISPLAY is
	// set, but key events and shortcuts reach only X11 windows there.
	Wayland
	// Native is a desktop with no X11 or Wayland: macOS and Windows.
	Native
)

func (k Kind) String() string {
	switch k {
	case X11:
		return "x11"
	case Wayland:
		return "wayland"
	case Native:
		return "native"
	default:
		return "none"
	}
}

// Current is the session this process runs in.
func Current() Kind { return Detect(runtime.GOOS, os.Getenv) }

// Detect returns the kind of session goos and the environment describe.
// On Linux and the BSDs, XDG_SESSION_TYPE decides when it says x11 or
// wayland; otherwise a WAYLAND_DISPLAY means Wayland and a DISPLAY X11.
// XDG_SESSION_TYPE comes first because a Wayland compositor nested in an
// X11 session, or an X11 app started from a Wayland terminal with DISPLAY
// pointing elsewhere, can leave both variables set.
func Detect(goos string, getenv func(string) string) Kind {
	if goos == "darwin" || goos == "windows" {
		return Native
	}
	switch strings.ToLower(strings.TrimSpace(getenv("XDG_SESSION_TYPE"))) {
	case "wayland":
		return Wayland
	case "x11":
		return X11
	}
	switch {
	case getenv("WAYLAND_DISPLAY") != "":
		return Wayland
	case getenv("DISPLAY") != "":
		return X11
	default:
		return None
	}
}

// Desktop names the desktop environment from XDG_CURRENT_DESKTOP, such as
// "GNOME" or "KDE", or "" when it is not set. A colon-separated list, as in
// "ubuntu:GNOME", gives its last entry, the desktop the others derive from.
func Desktop(getenv func(string) string) string {
	parts := strings.Split(getenv("XDG_CURRENT_DESKTOP"), ":")
	for i := len(parts) - 1; i >= 0; i-- {
		if p := strings.TrimSpace(parts[i]); p != "" {
			return p
		}
	}
	return ""
}
