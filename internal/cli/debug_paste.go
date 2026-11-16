package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/output"
)

func debugPaste(args []string, env Env) int {
	c := newCommand("debug paste", debugUsage, env)
	method := c.fs.String("method", "", "paste, direct or none")
	wait := c.fs.Duration("wait", 3*time.Second, "time to switch apps")
	keep := c.fs.Bool("keep", false, "leave the text on the clipboard")
	submit := c.fs.Bool("submit", false, "press Return afterwards")
	space := c.fs.Bool("space", false, "add a trailing space")
	keys := c.fs.String("keys", "", "Linux: ctrl_v, shift_insert or ctrl_shift_v")
	tool := c.fs.String("tool", "", "Linux: auto, xdotool, wtype, dotool or ydotool")
	pos, code, ok := c.parse(args)
	if !ok {
		return code
	}
	text := strings.Join(pos, " ")
	if strings.TrimSpace(text) == "" {
		return c.fail("no TEXT given")
	}
	s := userSettings()
	opts := output.OptionsFrom(s)
	switch m := config.PasteMethod(*method); m {
	case "":
	case config.PasteClipboard, config.PasteDirect, config.PasteNone:
		opts.Method = m
	default:
		return c.fail("--method %q: want paste, direct or none", *method)
	}
	switch k := config.PasteKeys(*keys); k {
	case "":
	case config.PasteCtrlV, config.PasteShiftInsert, config.PasteCtrlShiftV:
		s.PasteKeys = k
	default:
		return c.fail("--keys %q: want ctrl_v, shift_insert or ctrl_shift_v", *keys)
	}
	switch t := config.TypingTool(*tool); t {
	case "":
	case config.TypingAuto, config.TypingXdotool, config.TypingWtype, config.TypingDotool, config.TypingYdotool:
		s.TypingTool = t
	default:
		return c.fail("--tool %q: want auto, xdotool, wtype, dotool or ydotool", *tool)
	}
	if *keep {
		opts.Clipboard = config.ClipboardKeep
	}
	opts.AutoSubmit = opts.AutoSubmit || *submit
	opts.TrailingSpace = opts.TrailingSpace || *space
	if env.Output == nil {
		fmt.Fprintln(env.Stderr, "goscriber: this build cannot paste; build it with cgo on macOS or Linux")
		return exitFailure
	}
	oe := env.Output(s)

	how := map[config.PasteMethod]string{
		config.PasteClipboard: "paste with " + oe.PasteKey(),
		config.PasteDirect:    "type",
		config.PasteNone:      "copy",
	}[opts.Method]
	before, hadText := oe.Text()

	ctx, stop := interruptible()
	defer stop()
	if *wait > 0 {
		fmt.Fprintf(env.Stdout, "Switch to the app to put the text into: %s in %s.\n", how, *wait)
		select {
		case <-time.After(*wait):
		case <-ctx.Done():
			fmt.Fprintln(env.Stderr, "goscriber: interrupted")
			return exitInterrupted
		}
	}
	err := output.New(oe.Clipboard, oe.Keyboard, nil).Deliver(ctx, text, opts)
	after, hasText := oe.Text()
	sent := text
	if opts.TrailingSpace {
		sent += " "
	}
	clip := "holds something else"
	switch {
	case hasText == hadText && after == before:
		clip = "holds what it held before"
	case hasText && after == sent:
		clip = "holds the text"
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
		if errors.Is(err, output.ErrNoPermission) {
			fmt.Fprintln(env.Stderr, "goscriber: when run from a terminal, macOS asks the terminal app for the permission")
		}
		fmt.Fprintf(env.Stdout, "The clipboard %s.\n", clip)
		return exitFailure
	}
	verb := map[config.PasteMethod]string{config.PasteClipboard: "Pasted", config.PasteDirect: "Typed", config.PasteNone: "Copied"}[opts.Method]
	fmt.Fprintf(env.Stdout, "%s %d characters", verb, utf8.RuneCountInString(sent))
	if opts.AutoSubmit && opts.Method != config.PasteNone {
		fmt.Fprint(env.Stdout, " and pressed Return")
	}
	fmt.Fprintf(env.Stdout, ". The clipboard %s.\n", clip)
	return exitOK
}
