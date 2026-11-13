//go:build linux && cgo

package hotkey

import xhotkey "golang.design/x/hotkey"

// On X11, Alt is usually Mod1 and Super Mod4.
var systemModifiers = map[Modifier]xhotkey.Modifier{
	ModCtrl: xhotkey.ModCtrl, ModAlt: xhotkey.Mod1, ModShift: xhotkey.ModShift, ModSuper: xhotkey.Mod4,
}
