package hotkey

import "time"

// SecureInputChange is what a SecureInputWatch observation means for the
// user.
type SecureInputChange int

// Secure Event Input changes.
const (
	SecureInputUnchanged SecureInputChange = iota
	// SecureInputBlocking: Secure Event Input has stayed on long enough
	// to be more than a password field in passing, and the shortcut cannot
	// be heard until it goes off.
	SecureInputBlocking
	// SecureInputCleared: it went off after SecureInputBlocking; the
	// shortcut works again.
	SecureInputCleared
)

// DefaultSecureInputSustain is how long Secure Event Input must stay on
// before it is worth a warning, as in Handy.
const DefaultSecureInputSustain = 3 * time.Second

// SecureInputWatch decides when macOS Secure Event Input deserves a
// warning. On macOS the shortcuts are heard through a keyboard event tap,
// which receives no key presses while any process holds Secure Event Input.
// A focused password field turns it on for a moment, which is normal; held
// for Sustain it is probably stuck (Terminal's Secure Keyboard Entry, an
// app that forgot to turn it off). Feed it a reading about once a second.
type SecureInputWatch struct {
	// Sustain is how long it must stay on; zero means
	// DefaultSecureInputSustain.
	Sustain time.Duration

	on       bool
	since    time.Time
	blocking bool
}

// Observe records whether Secure Event Input is on at now and reports a
// change worth telling the user, once per change.
func (w *SecureInputWatch) Observe(on bool, now time.Time) SecureInputChange {
	sustain := w.Sustain
	if sustain <= 0 {
		sustain = DefaultSecureInputSustain
	}
	switch {
	case on && !w.on:
		w.on, w.since = true, now
	case !on && w.on:
		w.on = false
		if w.blocking {
			w.blocking = false
			return SecureInputCleared
		}
		return SecureInputUnchanged
	}
	if on && !w.blocking && now.Sub(w.since) >= sustain {
		w.blocking = true
		return SecureInputBlocking
	}
	return SecureInputUnchanged
}
