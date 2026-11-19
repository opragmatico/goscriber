//go:build unix

package hook_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/opragmatico/goscriber/hook"
)

// A Script runs any filter program as a hook, here a shell command.
func ExampleScript() {
	upper := hook.Script{Path: "/bin/sh", Args: []string{"-c", "tr a-z A-Z"}}
	text, err := upper.Rewrite(context.Background(), "hello from goscriber", hook.Info{})
	fmt.Println(text, err)
	// Output: HELLO FROM GOSCRIBER <nil>
}

// Rewrite text the way Goscriber rewrites a dictation before pasting it, with
// the hook installed in its config directory, if there is one.
func ExampleDefault() {
	text := "transcript of a voice memo"
	h, err := hook.Default()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// No hook: the text stays as it is.
	case err != nil:
		fmt.Println("the hook cannot run:", err)
	default:
		if out, err := h.Rewrite(context.Background(), text, hook.Info{Language: "en-US"}); err == nil {
			text = out
		}
	}
	fmt.Println(text)
}

// A hook program written in Go: build it to Goscriber's hooks directory as
// transcription, and every English dictation goes through polish before it
// is pasted. A rewrite that fails makes Goscriber paste the dictation as it
// was.
func ExampleMain() {
	hook.Main(hook.Func(func(ctx context.Context, text string, info hook.Info) (string, error) {
		if !strings.HasPrefix(info.Language, "en") {
			return text, nil
		}
		return polish(ctx, text)
	}))
}

// polish stands for a call to a workflow or an agent, such as one that turns
// notes into a polite email.
func polish(ctx context.Context, text string) (string, error) {
	return strings.ReplaceAll(text, "gonna", "going to"), ctx.Err()
}
