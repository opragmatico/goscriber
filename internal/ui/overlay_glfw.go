package ui

import "github.com/go-gl/glfw/v3.4/glfw"

// withoutFocusOnShow runs show, which may create a window, with GLFW told
// not to give windows it creates the keyboard focus when they are shown. A
// window keeps that from its creation, so the overlay never takes the focus
// from the app the user dictates into; later windows, such as the settings
// window, are created as usual.
//
// Fyne's driver owns GLFW and never sets this hint itself. Call it on the
// main thread once the app runs: before GLFW is initialised setting the hint
// fails, and show runs without it.
func withoutFocusOnShow(show func()) {
	if setWindowHint(glfw.FocusOnShow, glfw.False) {
		defer setWindowHint(glfw.FocusOnShow, glfw.True)
	}
	show()
}

// setWindowHint sets a GLFW window hint, reporting whether it could. The
// binding panics on any pending GLFW error, which must not take the app
// down.
func setWindowHint(hint glfw.Hint, value int) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	glfw.WindowHint(hint, value)
	return true
}
