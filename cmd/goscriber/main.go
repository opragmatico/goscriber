// Command goscriber is the Goscriber desktop app and its command-line tools.
package main

import (
	"os"

	"github.com/opragmatico/goscriber/engine"
	"github.com/opragmatico/goscriber/internal/cli"
	"github.com/opragmatico/goscriber/internal/config"
)

// version is stamped at link time: go build -ldflags "-X main.version=v1.2.3".
var version string

// runApp starts the desktop app. It is set in app.go for CGo builds; a
// CGO_ENABLED=0 build has no desktop app and leaves it nil.
var runApp func(cli.AppOptions) error

// newEngine returns the speech engine for `goscriber transcribe`, and
// engineVersion and engineGPU its version and GPU backend for the settings
// window. They are set in engine.go for builds with the NeMo-Speech.cpp
// SDK; other builds leave them nil.
var (
	newEngine     func() engine.Engine
	engineVersion func() string
	engineGPU     func() string
)

// newOutput reaches the clipboard and keyboard for `goscriber debug paste`.
// It is set in output.go for CGo builds; other builds leave it nil.
var newOutput func(config.Settings) cli.OutputEnv

func main() {
	// SIGUSR2 toggles dictation in the running app, which a Wayland
	// desktop's keyboard shortcut sends with pkill. Every other goscriber
	// process, the engine workers included, must ignore it rather than die
	// of it; the app listens for it again.
	ignoreToggleSignal()
	os.Exit(cli.Run(os.Args[1:], cli.Env{
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		Version:   version,
		RunApp:    runApp,
		NewEngine: newEngine,
		Output:    newOutput,
	}))
}
