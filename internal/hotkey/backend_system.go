//go:build (cgo && (darwin || linux)) || windows

package hotkey

import (
	"fmt"

	xhotkey "golang.design/x/hotkey"
)

// SystemBackend registers shortcuts through golang.design/x/hotkey: a
// keyboard event tap on macOS, which needs the Accessibility permission and
// hears nothing while Secure Event Input is on; XGrabKey on X11; and
// RegisterHotKey on Windows.
func SystemBackend() Backend { return systemBackend{} }

type systemBackend struct{}

// keyCodes maps a Binding's keys to the library's, whose names are the same
// on every OS.
var keyCodes = func() map[string]xhotkey.Key {
	m := map[string]xhotkey.Key{
		"space": xhotkey.KeySpace, "return": xhotkey.KeyReturn, "escape": xhotkey.KeyEscape, "tab": xhotkey.KeyTab,
		"up": xhotkey.KeyUp, "down": xhotkey.KeyDown, "left": xhotkey.KeyLeft, "right": xhotkey.KeyRight,
	}
	letters := []xhotkey.Key{
		xhotkey.KeyA, xhotkey.KeyB, xhotkey.KeyC, xhotkey.KeyD, xhotkey.KeyE, xhotkey.KeyF, xhotkey.KeyG,
		xhotkey.KeyH, xhotkey.KeyI, xhotkey.KeyJ, xhotkey.KeyK, xhotkey.KeyL, xhotkey.KeyM, xhotkey.KeyN,
		xhotkey.KeyO, xhotkey.KeyP, xhotkey.KeyQ, xhotkey.KeyR, xhotkey.KeyS, xhotkey.KeyT, xhotkey.KeyU,
		xhotkey.KeyV, xhotkey.KeyW, xhotkey.KeyX, xhotkey.KeyY, xhotkey.KeyZ,
	}
	for i, k := range letters {
		m[string(rune('a'+i))] = k
	}
	digits := []xhotkey.Key{
		xhotkey.Key0, xhotkey.Key1, xhotkey.Key2, xhotkey.Key3, xhotkey.Key4,
		xhotkey.Key5, xhotkey.Key6, xhotkey.Key7, xhotkey.Key8, xhotkey.Key9,
	}
	for i, k := range digits {
		m[string(rune('0'+i))] = k
	}
	fkeys := []xhotkey.Key{
		xhotkey.KeyF1, xhotkey.KeyF2, xhotkey.KeyF3, xhotkey.KeyF4, xhotkey.KeyF5, xhotkey.KeyF6, xhotkey.KeyF7,
		xhotkey.KeyF8, xhotkey.KeyF9, xhotkey.KeyF10, xhotkey.KeyF11, xhotkey.KeyF12, xhotkey.KeyF13, xhotkey.KeyF14,
		xhotkey.KeyF15, xhotkey.KeyF16, xhotkey.KeyF17, xhotkey.KeyF18, xhotkey.KeyF19, xhotkey.KeyF20,
	}
	for i, k := range fkeys {
		m[fmt.Sprintf("f%d", i+1)] = k
	}
	return m
}()

func (systemBackend) Register(b Binding) (Registration, error) {
	key, ok := keyCodes[b.Key]
	if !ok {
		return nil, fmt.Errorf("hotkey: no key code for %q", b.Key)
	}
	var mods []xhotkey.Modifier
	for _, m := range modifierOrder {
		if b.Has(m) {
			mods = append(mods, systemModifiers[m])
		}
	}
	hk := xhotkey.New(mods, key)
	if err := hk.Register(); err != nil {
		return nil, err
	}
	r := &systemRegistration{hk: hk, events: make(chan KeyEvent, 8)}
	go r.forward(hk.Keydown(), hk.Keyup())
	return r, nil
}

type systemRegistration struct {
	hk     *xhotkey.Hotkey
	events chan KeyEvent
}

func (r *systemRegistration) Events() <-chan KeyEvent { return r.events }

func (r *systemRegistration) Unregister() error { return r.hk.Unregister() }

// forward merges the library's two channels, which Unregister closes. A
// key-down is always taken before a key-up that is ready at the same time:
// the press came first.
func (r *systemRegistration) forward(down, up <-chan xhotkey.Event) {
	defer close(r.events)
	for down != nil || up != nil {
		select {
		case _, ok := <-down:
			if !ok {
				down = nil
				continue
			}
			r.events <- KeyDown
			continue
		default:
		}
		select {
		case _, ok := <-down:
			if !ok {
				down = nil
				continue
			}
			r.events <- KeyDown
		case _, ok := <-up:
			if !ok {
				up = nil
				continue
			}
			r.events <- KeyUp
		}
	}
}
