//go:build cgo

package main

import (
	"github.com/opragmatico/goscriber/internal/cli"
	"github.com/opragmatico/goscriber/internal/platform"
)

func init() {
	newOutput = func() cli.OutputEnv {
		pb := platform.GeneralPasteboard()
		return cli.OutputEnv{
			Clipboard: pb,
			Keyboard:  platform.Keyboard{},
			Text:      pb.Text,
			PasteKey:  platform.PasteKeyName,
		}
	}
}
