package worker

import (
	"os"
	"os/signal"

	"github.com/opragmatico/goscriber/internal/engine"
)

// Command is the hidden subcommand that turns the goscriber executable into
// an engine worker.
const Command = "engine-worker"

// Run is the worker process: it serves the protocol on stdin and stdout,
// hosting eng, and writes its own log to stderr, until the supervisor closes
// stdin. Anything else in the process that prints to stdout, such as the
// native engine, would corrupt the protocol, so on Unix the protocol moves
// to a copy of stdout and stdout itself is pointed at stderr.
func Run(eng engine.Engine) error {
	// Ctrl-C in a terminal reaches the whole process group. The app shuts
	// the worker down itself as it quits; the worker also exits when stdin
	// closes, so a parent that dies cannot leave it behind.
	signal.Ignore(os.Interrupt)
	out, err := protocolOutput()
	if err != nil {
		return err
	}
	log := NewWorkerLogger(os.Stderr)
	log.Debug("engine worker listening")
	return Serve(os.Stdin, out, eng, log)
}
