package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// Exit codes the subcommands share.
const (
	exitOK          = 0
	exitFailure     = 1
	exitUsage       = 2
	exitInterrupted = 130 // as a shell reports SIGINT
)

// command is a subcommand's flag set and usage text.
type command struct {
	fs    *flag.FlagSet
	usage string
	env   Env
}

func newCommand(name, usage string, env Env) *command {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return &command{fs: fs, usage: usage, env: env}
}

// parse parses args, letting flags and positional arguments come in any
// order ("pull MODEL --quant F16" as well as "pull --quant F16 MODEL"), and
// returns the positional ones. On -h it prints the usage to stdout; on an
// error it prints the error and the usage to stderr. ok is false in both
// cases, and code is the exit code to return.
func (c *command) parse(args []string) (pos []string, code int, ok bool) {
	for {
		if err := c.fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Fprint(c.env.Stdout, c.usage)
				return nil, exitOK, false
			}
			return nil, c.fail("%v", err), false
		}
		args = c.fs.Args()
		if len(args) == 0 {
			return pos, exitOK, true
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), exitOK, true
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// fail prints a usage error and returns exitUsage.
func (c *command) fail(format string, args ...any) int {
	fmt.Fprintf(c.env.Stderr, "goscriber %s: %s\n\n%s", c.fs.Name(), fmt.Sprintf(format, args...), c.usage)
	return exitUsage
}

// interruptible returns a context that is cancelled by Ctrl-C or SIGTERM.
func interruptible() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// isTerminal reports whether w is a terminal, where progress can redraw one
// line instead of printing many.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
