//go:build darwin && cgo

package hotkey

import xhotkey "golang.design/x/hotkey"

var systemModifiers = map[Modifier]xhotkey.Modifier{
	ModCtrl: xhotkey.ModCtrl, ModAlt: xhotkey.ModOption, ModShift: xhotkey.ModShift, ModSuper: xhotkey.ModCmd,
}
