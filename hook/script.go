package hook

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Reasons a Script fails, inside an *Error; test for them with errors.Is.
var (
	// ErrTimeout: the program ran past its timeout and was stopped.
	ErrTimeout = errors.New("timed out")
	// ErrOutputTooLarge: the program printed more than MaxOutput bytes.
	ErrOutputTooLarge = errors.New("printed more than 1 MiB")
)

// grace is how long a program that is asked to stop has to exit, and how long
// Rewrite waits for a program's output to close after the program has
// exited, since something it left running can hold the output open. After
// grace, os/exec kills the program and closes its output.
const grace = time.Second

// stderrTail is how much of what a program prints on standard error an
// *Error keeps: the end, where the reason for a failure usually is.
const stderrTail = 2 << 10

// Script is a hook program, run the way Goscriber runs the transcription hook
// (see the package documentation): the transcript on its standard input, its
// standard output as the text to use instead. Through Args it can run any
// filter program too, such as /opt/homebrew/bin/opencc with "-c" and
// "s2t.json".
//
// Rewrite fails with an *Error, whose Err is ErrTimeout when the program ran
// past its timeout, ErrOutputTooLarge when it printed more than MaxOutput
// bytes, an *exec.ExitError when it exited with a status other than 0, ctx's
// error when ctx ended first, or the reason it could not start.
//
// A program that times out, or whose ctx ends, is stopped along with whatever
// it started: outside Windows its whole process group gets SIGTERM, then
// SIGKILL a second later, so a hook can end what it started elsewhere, such
// as a workflow run, before it goes. On Windows the program alone is killed.
type Script struct {
	// Path is the program. A path without a separator is looked up in this
	// process's PATH.
	Path string
	// Args are its arguments; a transcription hook takes none.
	Args []string
	// Timeout is how long it may run; 0 means DefaultTimeout.
	Timeout time.Duration
	// Env is its environment, to which Info's variables are added; nil means
	// this process's environment.
	Env []string
}

// Rewrite implements Hook: it runs the program with text on its standard
// input and returns what the program printed, with one trailing newline (\n
// or \r\n) removed and invalid UTF-8 replaced by U+FFFD.
func (s Script) Rewrite(ctx context.Context, text string, info Info) (string, error) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	runCtx, cancel := context.WithTimeoutCause(ctx, timeout, ErrTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, s.Path, s.Args...)
	env := s.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(slices.Clip(env), info.environ()...)
	cmd.Stdin = strings.NewReader(text)
	stdout := &capped{max: MaxOutput}
	stderr := &tail{max: stderrTail}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = grace
	kill := stopGroup(cmd)

	err := cmd.Run()
	if runCtx.Err() != nil {
		// The program was asked to stop, and os/exec killed it if it did
		// not; take down what it started too.
		kill()
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// The program exited with status 0, but something it left running
		// held its output open: what the program printed is all there.
		err = nil
	}
	switch {
	case err != nil && runCtx.Err() != nil:
		err = context.Cause(runCtx)
	case err == nil && stdout.over:
		err = ErrOutputTooLarge
	}
	if err != nil {
		return "", &Error{Path: s.Path, Err: err, Stderr: stderr.String()}
	}
	return output(stdout.buf.String()), nil
}

// output is what a program printed as the text to use: without one trailing
// newline, \n or \r\n, and with invalid UTF-8 replaced.
func output(s string) string {
	if t, ok := strings.CutSuffix(s, "\n"); ok {
		s = strings.TrimSuffix(t, "\r")
	}
	return strings.ToValidUTF8(s, "�")
}

// Error is a hook program that failed.
type Error struct {
	// Path is the program.
	Path string
	// Err is why it failed; see Script.
	Err error
	// Stderr is the end of what it printed on standard error, trimmed.
	Stderr string
}

// Error says which program failed and why.
func (e *Error) Error() string { return "hook " + e.Path + ": " + e.Reason() }

// Reason is why the program failed, without its path: Err, then the last
// line the program printed on standard error, which usually says why.
func (e *Error) Reason() string {
	reason := e.Err.Error()
	if line := lastLine(e.Stderr); line != "" {
		reason += ": " + line
	}
	return reason
}

// Unwrap returns Err.
func (e *Error) Unwrap() error { return e.Err }

// lastLine is the last line of s that is not blank, trimmed.
func lastLine(s string) string {
	s = strings.TrimRight(s, " \t\r\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

// capped keeps the first max bytes written to it and drops the rest, noting
// that it did. It never refuses a write, so a program that prints too much
// is not left blocked on a full pipe.
type capped struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	n := len(p)
	if room := c.max - c.buf.Len(); len(p) > room {
		c.over = true
		p = p[:max(room, 0)]
	}
	c.buf.Write(p)
	return n, nil
}

// tail keeps the last max bytes written to it.
type tail struct {
	buf []byte
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > t.max {
		p = p[len(p)-t.max:]
	}
	if keep := t.max - len(p); len(t.buf) > keep {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-keep:]...)
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

// String is what was kept, trimmed, with invalid UTF-8 replaced, such as a
// character cut in two where the kept part starts.
func (t *tail) String() string {
	return strings.TrimSpace(strings.ToValidUTF8(string(t.buf), "�"))
}
