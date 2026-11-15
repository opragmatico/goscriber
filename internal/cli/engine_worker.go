package cli

import (
	"fmt"

	"github.com/opragmatico/goscriber/internal/engine/worker"
)

// runEngineWorker is the hidden engine-worker subcommand: the process the
// desktop app runs the speech engine in, so a crash in the engine cannot
// take the app down. It speaks the worker protocol on stdin and stdout and
// is not meant to be run by hand, so the usage leaves it out.
func runEngineWorker(args []string, env Env) int {
	if len(args) > 0 {
		fmt.Fprintf(env.Stderr, "goscriber %s: takes no arguments; the desktop app runs it\n", worker.Command)
		return exitUsage
	}
	if env.NewEngine == nil {
		fmt.Fprintf(env.Stderr, "goscriber %s: this build has no speech engine; build it with 'make deps build'\n", worker.Command)
		return exitFailure
	}
	if err := worker.Run(env.NewEngine()); err != nil {
		fmt.Fprintf(env.Stderr, "goscriber %s: %v\n", worker.Command, err)
		return exitFailure
	}
	return exitOK
}
