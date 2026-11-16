// Package typing drives the programs that press keys for Goscriber on
// Linux, the way Handy does: xdotool on X11, and wtype, dotool and ydotool,
// which also work on Wayland, where no app may send key presses to another
// by itself. Each presses the paste keys, types text and presses Return.
//
// Which works where:
//
//   - xdotool: X11. Under Wayland it reaches only X11 windows.
//   - wtype: Wayland compositors with the virtual keyboard protocol
//     (wlroots-based ones such as Sway and Hyprland, and KDE Plasma); not
//     GNOME.
//   - dotool: any session, through the kernel's uinput, which needs the
//     user in the input group.
//   - ydotool: any session, through uinput and its ydotoold daemon. Keys go
//     by their position on a US keyboard. Version 1.0 takes Linux key codes
//     ("29:1 47:1 47:0 29:0") and needs the daemon; 0.1, which Ubuntu 24.04
//     ships, takes key names ("CTRL_L+V"). Tool tries 1.0's form first and
//     0.1's when it fails, which it does before pressing anything (0.1
//     knows no key "29:1" and no option -f), and remembers the one that
//     worked.
//
// The package runs programs only; it needs no CGo.
package typing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/opragmatico/goscriber/internal/config"
)

// ErrNoTool means none of the typing tools that could press keys here is
// installed.
var ErrNoTool = errors.New("no typing tool is installed")

// WaylandTools are the tools that work on Wayland, in the order Chain tries
// them by default: wtype needs nothing set up where it works, dotool and
// ydotool work everywhere once uinput is allowed.
var WaylandTools = []config.TypingTool{config.TypingWtype, config.TypingDotool, config.TypingYdotool}

// Command is one run of a tool: its arguments, the first being the
// program, and what it reads on stdin.
type Command struct {
	Args  []string
	Stdin string
}

// Runner runs a command, giving up when ctx ends, and returns an error
// that says what the program wrote on stderr when it fails.
type Runner func(ctx context.Context, c Command) error

// Linux key codes (linux/input-event-codes.h), which ydotool takes.
const (
	keyEnter     = 28
	keyLeftCtrl  = 29
	keyLeftShift = 42
	keyV         = 47
	keyInsert    = 110
)

// PasteCommand is the command with which tool presses keys.
func PasteCommand(tool config.TypingTool, keys config.PasteKeys) (Command, error) {
	type combo struct {
		xdotool, dotool string
		wtype           []string
		ydotool         []int
	}
	var c combo
	switch keys {
	case config.PasteCtrlV:
		c = combo{"ctrl+v", "ctrl+v", []string{"-M", "ctrl", "-k", "v", "-m", "ctrl"}, []int{keyLeftCtrl, keyV}}
	case config.PasteShiftInsert:
		c = combo{"shift+Insert", "shift+insert", []string{"-M", "shift", "-k", "Insert", "-m", "shift"}, []int{keyLeftShift, keyInsert}}
	case config.PasteCtrlShiftV:
		c = combo{
			"ctrl+shift+v", "ctrl+shift+v",
			[]string{"-M", "ctrl", "-M", "shift", "-k", "v", "-m", "shift", "-m", "ctrl"},
			[]int{keyLeftCtrl, keyLeftShift, keyV},
		}
	default:
		return Command{}, fmt.Errorf("typing: unknown paste keys %q", keys)
	}
	switch tool {
	case config.TypingXdotool:
		return Command{Args: []string{"xdotool", "key", "--clearmodifiers", c.xdotool}}, nil
	case config.TypingWtype:
		return Command{Args: append([]string{"wtype"}, c.wtype...)}, nil
	case config.TypingDotool:
		return Command{Args: []string{"dotool"}, Stdin: "key " + c.dotool + "\n"}, nil
	case config.TypingYdotool:
		return Command{Args: append([]string{"ydotool", "key"}, chord(c.ydotool...)...)}, nil
	default:
		return Command{}, unknownTool(tool)
	}
}

// ReturnCommand is the command with which tool presses Return.
func ReturnCommand(tool config.TypingTool) (Command, error) {
	switch tool {
	case config.TypingXdotool:
		return Command{Args: []string{"xdotool", "key", "--clearmodifiers", "Return"}}, nil
	case config.TypingWtype:
		return Command{Args: []string{"wtype", "-k", "Return"}}, nil
	case config.TypingDotool:
		return Command{Args: []string{"dotool"}, Stdin: "key enter\n"}, nil
	case config.TypingYdotool:
		return Command{Args: append([]string{"ydotool", "key"}, chord(keyEnter)...)}, nil
	default:
		return Command{}, unknownTool(tool)
	}
}

// TypeCommand is the command with which tool types text.
func TypeCommand(tool config.TypingTool, text string) (Command, error) {
	switch tool {
	case config.TypingXdotool:
		return Command{Args: []string{"xdotool", "type", "--clearmodifiers", "--delay", "5", "--", text}}, nil
	case config.TypingWtype:
		// "-" reads the text from stdin, so it cannot be taken for an
		// option whatever it starts with.
		return Command{Args: []string{"wtype", "-"}, Stdin: text}, nil
	case config.TypingDotool:
		// dotool reads one command a line, so a line break is a Return.
		var b strings.Builder
		for i, line := range strings.Split(text, "\n") {
			if i > 0 {
				b.WriteString("key enter\n")
			}
			if line != "" {
				b.WriteString("type " + line + "\n")
			}
		}
		return Command{Args: []string{"dotool"}, Stdin: b.String()}, nil
	case config.TypingYdotool:
		// -f - reads the text from stdin.
		return Command{Args: []string{"ydotool", "type", "-f", "-"}, Stdin: text}, nil
	default:
		return Command{}, unknownTool(tool)
	}
}

// ydotool 0.1's commands: key names joined with "+", from its own table
// (CTRL_L, SHIFT_L, INSERT, ENTER) or Linux's without KEY_ (V), and the
// text as an argument.
func legacyYdotoolPaste(keys config.PasteKeys) Command {
	chord := map[config.PasteKeys]string{
		config.PasteCtrlV: "CTRL_L+V", config.PasteShiftInsert: "SHIFT_L+INSERT", config.PasteCtrlShiftV: "CTRL_L+SHIFT_L+V",
	}[keys]
	return Command{Args: []string{"ydotool", "key", chord}}
}

func legacyYdotoolReturn() Command { return Command{Args: []string{"ydotool", "key", "ENTER"}} }

func legacyYdotoolType(text string) Command {
	return Command{Args: []string{"ydotool", "type", text}}
}

// ydotoolLegacy remembers that the installed ydotool is version 0.1: its
// form of a command worked where 1.0's did not.
var ydotoolLegacy atomic.Bool

// chord presses keys in order and lets go of them in reverse, in ydotool's
// "code:1" (down) and "code:0" (up) notation.
func chord(keys ...int) []string {
	out := make([]string, 0, 2*len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%d:1", k))
	}
	for _, k := range slices.Backward(keys) {
		out = append(out, fmt.Sprintf("%d:0", k))
	}
	return out
}

func unknownTool(tool config.TypingTool) error {
	return fmt.Errorf("typing: %q is not a typing tool", tool)
}

// Exec is the Runner that runs programs. It keeps the last line the program
// wrote on stderr for the error, which is where every tool says what went
// wrong ("Compositor does not support the virtual keyboard protocol").
func Exec(ctx context.Context, c Command) error {
	if len(c.Args) == 0 {
		return errors.New("typing: empty command")
	}
	cmd := exec.CommandContext(ctx, c.Args[0], c.Args[1:]...)
	cmd.Stdin = strings.NewReader(c.Stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &limited{w: &stderr, n: 4096}
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if msg := lastLine(stderr.String()); msg != "" {
			return fmt.Errorf("%s: %w: %s", c.Args[0], err, msg)
		}
		return fmt.Errorf("%s: %w", c.Args[0], err)
	}
	return nil
}

// limited writes at most n bytes to w and drops the rest.
type limited struct {
	w *bytes.Buffer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.n - l.w.Len(); room > 0 {
		l.w.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Timeouts: a tool that has not pressed its keys by then is stuck, as
// ydotool is when its daemon does not answer.
const (
	keyTimeout     = 5 * time.Second
	perRuneTimeout = 40 * time.Millisecond // ydotool's default delays are 32 ms a key
)

// Tool presses keys through one program.
type Tool struct {
	Name config.TypingTool
	// Run runs the program; nil means Exec.
	Run Runner
}

// run runs c, or for ydotool c and then legacy, 0.1's form of the same
// command, when c fails: in the order that worked last.
func (t Tool) run(c Command, err error, legacy Command, timeout time.Duration) error {
	if err != nil {
		return err
	}
	run := t.Run
	if run == nil {
		run = Exec
	}
	try := func(c Command) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return run(ctx, c)
	}
	if t.Name != config.TypingYdotool {
		return try(c)
	}
	forms := []Command{c, legacy}
	if ydotoolLegacy.Load() {
		forms = []Command{legacy, c}
	}
	first := try(forms[0])
	if first == nil {
		return nil
	}
	if second := try(forms[1]); second != nil {
		return errors.Join(first, second)
	}
	ydotoolLegacy.Store(!ydotoolLegacy.Load())
	return nil
}

// Paste presses keys.
func (t Tool) Paste(keys config.PasteKeys) error {
	c, err := PasteCommand(t.Name, keys)
	return t.run(c, err, legacyYdotoolPaste(keys), keyTimeout)
}

// PressReturn presses Return.
func (t Tool) PressReturn() error {
	c, err := ReturnCommand(t.Name)
	return t.run(c, err, legacyYdotoolReturn(), keyTimeout)
}

// Type types text.
func (t Tool) Type(text string) error {
	c, err := TypeCommand(t.Name, text)
	return t.run(c, err, legacyYdotoolType(text), keyTimeout+time.Duration(utf8.RuneCountInString(text))*perRuneTimeout)
}

// Chain presses keys through the first of its tools that works, which is
// what the auto typing tool does on Wayland: wtype fails at once on GNOME,
// whose compositor has no virtual keyboard, and dotool and ydotool fail at
// once without access to uinput. The tool that worked is tried first the
// next time. Tools that are not installed are skipped. The zero value is
// not usable; call NewChain.
type Chain struct {
	tools    []config.TypingTool
	run      Runner
	lookPath func(string) (string, error)

	mu      sync.Mutex
	working config.TypingTool // the tool that worked last
}

// NewChain returns a Chain over tools, in order. A nil run means Exec and a
// nil lookPath exec.LookPath.
func NewChain(tools []config.TypingTool, run Runner, lookPath func(string) (string, error)) *Chain {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	return &Chain{tools: slices.Clone(tools), run: run, lookPath: lookPath}
}

// Paste presses keys through the first tool that can.
func (c *Chain) Paste(keys config.PasteKeys) error {
	return c.do(func(t Tool) error { return t.Paste(keys) })
}

// PressReturn presses Return through the first tool that can.
func (c *Chain) PressReturn() error { return c.do(Tool.PressReturn) }

// Type types text through the first tool that can.
func (c *Chain) Type(text string) error {
	return c.do(func(t Tool) error { return t.Type(text) })
}

// Working is the tool that worked last, or "" before any has.
func (c *Chain) Working() config.TypingTool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.working
}

func (c *Chain) do(f func(Tool) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	order := Installed(c.lookPath, c.tools...)
	if i := slices.Index(order, c.working); i > 0 {
		order = append([]config.TypingTool{c.working}, slices.Delete(order, i, i+1)...)
	}
	if len(order) == 0 {
		return fmt.Errorf("%w: install %s", ErrNoTool, Names(c.tools))
	}
	var errs []error
	for _, name := range order {
		err := f(Tool{Name: name, Run: c.run})
		if err == nil {
			c.working = name
			return nil
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Installed returns the tools whose program lookPath finds, in order.
func Installed(lookPath func(string) (string, error), tools ...config.TypingTool) []config.TypingTool {
	var out []config.TypingTool
	for _, t := range tools {
		if _, err := lookPath(string(t)); err == nil {
			out = append(out, t)
		}
	}
	return out
}

// Names lists tools for a message: "wtype, dotool or ydotool".
func Names(tools []config.TypingTool) string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = string(t)
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
	}
}
