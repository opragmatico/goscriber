// Package cli parses Goscriber's command line: global flags today, and later
// subcommands (transcribe, models, engine-worker) and the remote-control flags
// that are forwarded to a running instance.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"
)

// Env carries everything Run needs from the process, so tests can supply their
// own writers, version and app.
type Env struct {
	Stdout  io.Writer
	Stderr  io.Writer
	Version string // set by main through -ldflags; empty means "use build info"
	// RunApp starts the desktop app. main wires it to internal/app, which keeps
	// this package free of the app's CGo dependencies. Nil prints a notice.
	RunApp func(AppOptions) error
}

// AppOptions are the startup flags handed to the desktop app.
type AppOptions struct {
	Debug   bool
	Version string
}

const usage = `Goscriber: offline push-to-talk speech-to-text.

Usage:
  goscriber [flags]

Flags:
      --debug     verbose logging and debug mode for this session
  -h, --help      show this help and exit
      --version   print the version and exit

Environment:
  GOSCRIBER_LOG   log level override: debug, info, warn or error
  GOSCRIBER_HOME  keep settings, models, history and logs in this directory
`

// Run parses args (without the program name) and returns the process exit code.
func Run(args []string, env Env) int {
	fs := flag.NewFlagSet("goscriber", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	showVersion := fs.Bool("version", false, "print the version and exit")
	debug := fs.Bool("debug", false, "verbose logging and debug mode for this session")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(env.Stdout, usage)
			return 0
		}
		fmt.Fprintf(env.Stderr, "goscriber: %v\n\n%s", err, usage)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(env.Stderr, "goscriber: unexpected argument %q\n\n%s", fs.Arg(0), usage)
		return 2
	}

	if *showVersion {
		fmt.Fprintf(env.Stdout, "goscriber %s (%s %s/%s)\n",
			ResolveVersion(env.Version), runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return 0
	}

	if env.RunApp == nil {
		fmt.Fprintln(env.Stderr, "goscriber: the desktop app is not available in this build; run 'goscriber --help'")
		return 0
	}
	if err := env.RunApp(AppOptions{Debug: *debug, Version: ResolveVersion(env.Version)}); err != nil {
		fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
		return 1
	}
	return 0
}
