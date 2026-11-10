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
// own writers and version.
type Env struct {
	Stdout  io.Writer
	Stderr  io.Writer
	Version string // set by main through -ldflags; empty means "use build info"
}

const usage = `Goscriber: offline push-to-talk speech-to-text.

Usage:
  goscriber [flags]

Flags:
  -h, --help      show this help and exit
      --version   print the version and exit
`

// Run parses args (without the program name) and returns the process exit code.
func Run(args []string, env Env) int {
	fs := flag.NewFlagSet("goscriber", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	showVersion := fs.Bool("version", false, "print the version and exit")

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

	fmt.Fprintln(env.Stderr, "goscriber: the desktop app is not implemented yet "+
		"(https://github.com/opragmatico/goscriber/issues/6); run 'goscriber --help'")
	return 0
}
