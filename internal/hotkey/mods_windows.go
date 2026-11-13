//go:build windows

package hotkey

import xhotkey "golang.design/x/hotkey"

var systemModifiers = map[Modifier]xhotkey.Modifier{
	ModCtrl: xhotkey.ModCtrl, ModAlt: xhotkey.ModAlt, ModShift: xhotkey.ModShift, ModSuper: xhotkey.ModWin,
}
