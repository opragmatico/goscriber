// Package x11test plays another app on an X server, for the tests of
// Goscriber's X11 clipboard and key events on Linux: a focused window that
// records what is typed into it and pastes the clipboard when it gets the
// paste keys, as a text field would, and a client that copies text. It
// needs a running X server (DISPLAY), such as Xvfb, and is empty on other
// systems.
package x11test
