package typing

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/opragmatico/goscriber/internal/config"
)

func TestPasteCommands(t *testing.T) {
	tests := []struct {
		tool  config.TypingTool
		keys  config.PasteKeys
		args  []string
		stdin string
	}{
		{config.TypingXdotool, config.PasteCtrlV, []string{"xdotool", "key", "--clearmodifiers", "ctrl+v"}, ""},
		{config.TypingXdotool, config.PasteShiftInsert, []string{"xdotool", "key", "--clearmodifiers", "shift+Insert"}, ""},
		{config.TypingWtype, config.PasteCtrlV, []string{"wtype", "-M", "ctrl", "-k", "v", "-m", "ctrl"}, ""},
		{config.TypingWtype, config.PasteCtrlShiftV, []string{"wtype", "-M", "ctrl", "-M", "shift", "-k", "v", "-m", "shift", "-m", "ctrl"}, ""},
		{config.TypingDotool, config.PasteShiftInsert, []string{"dotool"}, "key shift+insert\n"},
		{config.TypingDotool, config.PasteCtrlShiftV, []string{"dotool"}, "key ctrl+shift+v\n"},
		// Linux key codes: down in order, up in reverse.
		{config.TypingYdotool, config.PasteCtrlV, []string{"ydotool", "key", "29:1", "47:1", "47:0", "29:0"}, ""},
		{config.TypingYdotool, config.PasteShiftInsert, []string{"ydotool", "key", "42:1", "110:1", "110:0", "42:0"}, ""},
		{config.TypingYdotool, config.PasteCtrlShiftV, []string{"ydotool", "key", "29:1", "42:1", "47:1", "47:0", "42:0", "29:0"}, ""},
	}
	for _, tt := range tests {
		c, err := PasteCommand(tt.tool, tt.keys)
		if err != nil || !reflect.DeepEqual(c.Args, tt.args) || c.Stdin != tt.stdin {
			t.Errorf("PasteCommand(%s, %s) = %q %q, %v; want %q %q", tt.tool, tt.keys, c.Args, c.Stdin, err, tt.args, tt.stdin)
		}
	}
	if _, err := PasteCommand(config.TypingAuto, config.PasteCtrlV); err == nil {
		t.Error("auto is no program, but PasteCommand made a command for it")
	}
	if _, err := PasteCommand(config.TypingWtype, "alt_v"); err == nil {
		t.Error("PasteCommand accepted unknown paste keys")
	}
}

func TestReturnCommands(t *testing.T) {
	want := map[config.TypingTool]Command{
		config.TypingXdotool: {Args: []string{"xdotool", "key", "--clearmodifiers", "Return"}},
		config.TypingWtype:   {Args: []string{"wtype", "-k", "Return"}},
		config.TypingDotool:  {Args: []string{"dotool"}, Stdin: "key enter\n"},
		config.TypingYdotool: {Args: []string{"ydotool", "key", "28:1", "28:0"}},
	}
	for tool, w := range want {
		if c, err := ReturnCommand(tool); err != nil || !reflect.DeepEqual(c, w) {
			t.Errorf("ReturnCommand(%s) = %+v, %v; want %+v", tool, c, err, w)
		}
	}
}

func TestTypeCommands(t *testing.T) {
	text := "-n Größe\nzwei"
	want := map[config.TypingTool]Command{
		// "--" and stdin keep text that looks like an option from being
		// taken for one.
		config.TypingXdotool: {Args: []string{"xdotool", "type", "--clearmodifiers", "--delay", "5", "--", text}},
		config.TypingWtype:   {Args: []string{"wtype", "-"}, Stdin: text},
		config.TypingDotool:  {Args: []string{"dotool"}, Stdin: "type -n Größe\nkey enter\ntype zwei\n"},
		config.TypingYdotool: {Args: []string{"ydotool", "type", "-f", "-"}, Stdin: text},
	}
	for tool, w := range want {
		if c, err := TypeCommand(tool, text); err != nil || !reflect.DeepEqual(c, w) {
			t.Errorf("TypeCommand(%s) = %+v, %v; want %+v", tool, c, err, w)
		}
	}
	c, _ := TypeCommand(config.TypingDotool, "a\n\nb\n")
	if want := "type a\nkey enter\nkey enter\ntype b\nkey enter\n"; c.Stdin != want {
		t.Errorf("dotool, empty lines: stdin %q, want %q", c.Stdin, want)
	}
}

// fakeTools pretends some tools are installed and records what ran; the
// tools in failing fail.
type fakeTools struct {
	installed map[string]bool
	failing   map[string]bool
	ran       []string
}

func (f *fakeTools) lookPath(name string) (string, error) {
	if f.installed[name] {
		return "/usr/bin/" + name, nil
	}
	return "", exec.ErrNotFound
}

func (f *fakeTools) run(_ context.Context, c Command) error {
	f.ran = append(f.ran, c.Args[0])
	if f.failing[c.Args[0]] {
		return errors.New(c.Args[0] + ": exit status 1: it cannot work here")
	}
	return nil
}

// On GNOME, wtype is installed but fails: the chain goes on to the next
// tool, and from then on starts with the one that worked.
func TestChainFallsBackAndRemembers(t *testing.T) {
	f := &fakeTools{
		installed: map[string]bool{"wtype": true, "ydotool": true},
		failing:   map[string]bool{"wtype": true},
	}
	c := NewChain(WaylandTools, f.run, f.lookPath)
	if err := c.Paste(config.PasteShiftInsert); err != nil {
		t.Fatalf("Paste: %v", err)
	}
	if want := []string{"wtype", "ydotool"}; !reflect.DeepEqual(f.ran, want) {
		t.Errorf("ran %q, want %q (dotool is not installed)", f.ran, want)
	}
	if c.Working() != config.TypingYdotool {
		t.Errorf("Working = %q, want ydotool", c.Working())
	}
	f.ran = nil
	if err := c.Type("hello"); err != nil || !reflect.DeepEqual(f.ran, []string{"ydotool"}) {
		t.Errorf("Type: ran %q, %v; want ydotool alone", f.ran, err)
	}
	// The tool that worked stops working: the others get their turn again
	// (ydotool after trying both of its forms).
	f.failing = map[string]bool{"ydotool": true}
	f.ran = nil
	if err := c.PressReturn(); err != nil || !reflect.DeepEqual(f.ran, []string{"ydotool", "ydotool", "wtype"}) {
		t.Errorf("PressReturn: ran %q, %v; want ydotool twice, then wtype", f.ran, err)
	}
}

// Ubuntu 24.04's ydotool 0.1 refuses 1.0's key codes and -f: Tool falls
// back to 0.1's key names, and starts with them from then on.
func TestYdotoolVersion01(t *testing.T) {
	t.Cleanup(func() { ydotoolLegacy.Store(false) })
	var ran [][]string
	v01 := func(_ context.Context, c Command) error {
		ran = append(ran, c.Args)
		if slices.Contains(c.Args, "-f") || slices.Contains(c.Args, "29:1") || slices.Contains(c.Args, "42:1") || slices.Contains(c.Args, "28:1") {
			return errors.New("ydotool: exit status 1: invalid key")
		}
		return nil
	}
	tool := Tool{Name: config.TypingYdotool, Run: v01}
	if err := tool.Paste(config.PasteShiftInsert); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"ydotool", "key", "42:1", "110:1", "110:0", "42:0"},
		{"ydotool", "key", "SHIFT_L+INSERT"},
	}
	if !reflect.DeepEqual(ran, want) {
		t.Errorf("first paste ran %q, want %q", ran, want)
	}
	ran = nil
	if err := tool.Type("-x"); err != nil || !reflect.DeepEqual(ran, [][]string{{"ydotool", "type", "-x"}}) {
		t.Errorf("then Type ran %q, %v; want 0.1's form alone", ran, err)
	}
	ran = nil
	_ = tool.PressReturn()
	if !reflect.DeepEqual(ran, [][]string{{"ydotool", "key", "ENTER"}}) {
		t.Errorf("then PressReturn ran %q", ran)
	}
	if c := legacyYdotoolPaste(config.PasteCtrlShiftV); !reflect.DeepEqual(c.Args, []string{"ydotool", "key", "CTRL_L+SHIFT_L+V"}) {
		t.Errorf("0.1's Ctrl+Shift+V: %q", c.Args)
	}

	// A 1.0 whose daemon is down: both forms fail, and both errors say so.
	ydotoolLegacy.Store(false)
	down := func(context.Context, Command) error { return errors.New("ydotool: failed to connect socket") }
	err := Tool{Name: config.TypingYdotool, Run: down}.PressReturn()
	if err == nil || strings.Count(err.Error(), "failed to connect socket") != 2 || ydotoolLegacy.Load() {
		t.Errorf("both forms failing: %v (legacy remembered: %v)", err, ydotoolLegacy.Load())
	}
}

func TestChainWithoutTools(t *testing.T) {
	f := &fakeTools{}
	err := NewChain(WaylandTools, f.run, f.lookPath).Type("x")
	if !errors.Is(err, ErrNoTool) || !strings.Contains(err.Error(), "wtype, dotool or ydotool") {
		t.Errorf("Type with nothing installed: %v; want ErrNoTool naming the tools", err)
	}
	if len(f.ran) > 0 {
		t.Errorf("ran %q with nothing installed", f.ran)
	}
}

func TestChainReportsEveryFailure(t *testing.T) {
	f := &fakeTools{
		installed: map[string]bool{"wtype": true, "dotool": true},
		failing:   map[string]bool{"wtype": true, "dotool": true},
	}
	err := NewChain(WaylandTools, f.run, f.lookPath).Paste(config.PasteCtrlV)
	if err == nil || !strings.Contains(err.Error(), "wtype:") || !strings.Contains(err.Error(), "dotool:") {
		t.Errorf("Paste with every tool failing: %v; want both failures", err)
	}
}

func TestNames(t *testing.T) {
	for want, tools := range map[string][]config.TypingTool{
		"":                         nil,
		"wtype":                    {config.TypingWtype},
		"wtype or dotool":          {config.TypingWtype, config.TypingDotool},
		"wtype, dotool or ydotool": WaylandTools,
	} {
		if got := Names(tools); got != want {
			t.Errorf("Names(%q) = %q, want %q", tools, got, want)
		}
	}
}

// Exec runs the program with the command's stdin and reports its last line
// of stderr when it fails.
func TestExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "tool")
	body := "#!/bin/sh\nread line\n[ \"$line\" = ok ] && exit 0\necho warming up >&2\necho \"cannot type: $line\" >&2\nexit 3\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Exec(context.Background(), Command{Args: []string{script}, Stdin: "ok\n"}); err != nil {
		t.Errorf("Exec of a working tool: %v", err)
	}
	err := Exec(context.Background(), Command{Args: []string{script}, Stdin: "nope\n"})
	if err == nil || !strings.Contains(err.Error(), "cannot type: nope") || strings.Contains(err.Error(), "warming up") {
		t.Errorf("Exec of a failing tool: %v; want its last stderr line", err)
	}
	if err := Exec(context.Background(), Command{Args: []string{filepath.Join(dir, "missing")}}); err == nil {
		t.Error("Exec of a missing program succeeded")
	}
}
