//go:build cgo

package main

import (
	"github.com/opragmatico/goscriber/internal/app"
	"github.com/opragmatico/goscriber/internal/cli"
)

func init() {
	runApp = func(o cli.AppOptions) error {
		return app.Run(app.Options{
			Debug:       o.Debug,
			StartHidden: o.StartHidden,
			NoTray:      o.NoTray,
			Version:     o.Version,
			NewEngine:   newEngine,
		})
	}
}
