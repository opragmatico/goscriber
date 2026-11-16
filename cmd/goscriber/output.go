//go:build cgo

package main

import (
	"github.com/opragmatico/goscriber/internal/cli"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/platform"
)

func init() {
	newOutput = func(s config.Settings) cli.OutputEnv {
		pb := platform.GeneralPasteboard()
		kb := platform.Keyboard{Settings: func() config.Settings { return s }}
		return cli.OutputEnv{
			Clipboard: pb,
			Keyboard:  kb,
			Text:      pb.Text,
			PasteKey:  kb.PasteKeyName,
		}
	}
}
