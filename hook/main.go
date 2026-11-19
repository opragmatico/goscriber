package hook

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

// Run is the hook program's end of the contract: it reads the transcript
// from in, rewrites it with h, given the Info in the environment, and writes
// the result to out as it is, adding no newline. Main runs it on the
// standard streams.
func Run(ctx context.Context, h Hook, in io.Reader, out io.Writer) error {
	b, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("hook: read the transcript: %w", err)
	}
	text, err := h.Rewrite(ctx, string(b), infoFromEnv(os.Getenv))
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, text)
	return err
}

// Main makes this program a hook that rewrites with h: it reads the
// transcript from standard input, writes h's result to standard output and
// returns. When h fails, Main prints the error on standard error and exits
// with status 1, so Goscriber pastes the original transcript. h's context
// ends on an interrupt or SIGTERM, which Goscriber sends when the hook times
// out or the dictation is cancelled, a second before it kills the hook.
//
// A program that hands the transcript to a workflow and prints its answer
// is a hook in a few lines:
//
//	func main() {
//		hook.Main(hook.Func(func(ctx context.Context, text string, info hook.Info) (string, error) {
//			return rewriteWithWorkflow(ctx, text, info.Language)
//		}))
//	}
func Main(h Hook) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := Run(ctx, h, os.Stdin, os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", filepath.Base(os.Args[0]), err)
		os.Exit(1)
	}
}
