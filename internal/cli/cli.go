// Package cli parses Goscriber's command line: the desktop app's flags, the
// transcribe, models and debug subcommands, the hidden engine-worker
// subcommand the app runs its speech engine in, and the remote-control
// flags that are forwarded to the running app.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/worker"
	"github.com/opragmatico/goscriber/internal/instance"
	"github.com/opragmatico/goscriber/internal/models"
	"github.com/opragmatico/goscriber/internal/output"
)

// Env carries everything Run needs from the process, so tests can supply their
// own writers, version and app.
type Env struct {
	Stdout  io.Writer
	Stderr  io.Writer
	Version string // set by main through -ldflags; empty means "use build info"
	// RunApp starts the desktop app. main wires it to internal/app, which keeps
	// this package free of the app's CGo dependencies. Nil prints a notice.
	RunApp func(AppOptions) error
	// NewEngine returns the speech engine transcribe loads models into. main
	// wires it to the NeMo-Speech.cpp binding in builds that have it; nil
	// means this build has no engine.
	NewEngine func() engine.Engine
	// Output reaches the clipboard and keyboard for `goscriber debug paste`,
	// pressing keys as s says (paste_keys and typing_tool on Linux). main
	// wires it to internal/platform in CGo builds; nil means this build
	// cannot paste.
	Output func(s config.Settings) OutputEnv
	// Models manages the model files for the subcommands. Nil means one for
	// the user's models directory, which also looks in the Hugging Face
	// cache; tests supply their own.
	Models *models.Manager
}

// OutputEnv is the system clipboard and keyboard, as `goscriber debug paste`
// uses them.
type OutputEnv struct {
	Clipboard output.Clipboard
	Keyboard  output.Keyboard
	// Text reads the clipboard's plain text, to tell whether a paste put it
	// back. Its content is never printed.
	Text func() (string, bool)
	// PasteKey describes the paste shortcut in the active keyboard layout,
	// such as "Cmd+V (key code 9)", or the paste keys and what presses
	// them on Linux, such as "Shift+Insert (X11 key events)".
	PasteKey func() string
}

// userSettings returns the saved settings, or the defaults when there are
// none or they cannot be read.
func userSettings() config.Settings {
	if paths, err := config.ResolvePaths(); err == nil {
		if s, _, err := config.Load(paths.SettingsFile()); err == nil {
			return s
		}
	}
	return config.Defaults()
}

// modelManager returns env.Models, or a Manager for the user's models
// directory.
func (env Env) modelManager() (*models.Manager, error) {
	if env.Models != nil {
		return env.Models, nil
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		return nil, err
	}
	return models.NewManager(paths.ModelsDir(), models.Options{
		HFCache:    models.HFCacheDir(os.Getenv),
		Endpoint:   models.EndpointFromEnv(os.Getenv),
		Downloader: &models.Downloader{UserAgent: "goscriber/" + ResolveVersion(env.Version)},
	}), nil
}

// AppOptions are the startup flags handed to the desktop app.
type AppOptions struct {
	Debug       bool
	StartHidden bool // open no window at launch, whatever the start_hidden setting says
	NoTray      bool // run without the tray icon, whatever the show_tray_icon setting says
	// EngineInProcess runs the speech engine inside the app rather than in
	// a worker process, whatever the engine_in_process setting says.
	EngineInProcess bool
	Version         string
	// Instance makes this launch the only Goscriber running; the app
	// answers the commands later launches send it. Nil where the system
	// has no single-instance support yet, or the lock could not be taken.
	// Run releases it once the app returns.
	Instance *instance.Instance
}

const usage = `Goscriber: offline push-to-talk speech-to-text.

Usage:
  goscriber [flags]            start the desktop app, or show the running one
  goscriber --toggle-transcription | --cancel
                               control the running app
  goscriber transcribe FILE... transcribe WAV or FLAC files and print the text
  goscriber models COMMAND     list, download and delete speech models

Commands:
  models list                  show each model and whether it is downloaded
  models pull MODEL            download a model, with the voice activity detector
  models rm MODEL              delete a model's files
  debug devices                list the audio devices
  debug record FILE.wav        record the microphone to a file, and check it

Run 'goscriber COMMAND --help' for a command's flags.

Flags:
      --debug          verbose logging and debug mode for this session
      --engine-in-process
                       run the speech engine inside the app, for profiling;
                       a crash in the engine then ends the app
  -h, --help           show this help and exit
      --no-tray        run without the tray icon; the settings window opens instead
      --start-hidden   open no window at launch
      --version        print the version and exit

Remote control (sent to the running app, which must be started first):
      --toggle-transcription
                       start dictation, or stop it and paste the transcript
      --cancel         discard the dictation in progress
  Sending SIGUSR2 to the app also toggles dictation.

Only one Goscriber runs at a time: starting it again without a remote-control
flag brings the running one's settings window to the front.

Environment:
  GOSCRIBER_LOG   log level override: debug, info, warn or error
  GOSCRIBER_HOME  keep settings, models, history and logs in this directory
  HF_HOME, HF_HUB_CACHE
                  the Hugging Face cache, where existing model files are reused
  HF_ENDPOINT     download from this Hugging Face mirror
`

// Run parses args (without the program name) and returns the process exit code.
func Run(args []string, env Env) int {
	if len(args) > 0 {
		switch args[0] {
		case "models":
			return runModels(args[1:], env)
		case "transcribe":
			return runTranscribe(args[1:], env)
		case "debug":
			return runDebug(args[1:], env)
		case worker.Command:
			return runEngineWorker(args[1:], env)
		case "help":
			fmt.Fprint(env.Stdout, usage)
			return 0
		}
	}

	fs := flag.NewFlagSet("goscriber", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	showVersion := fs.Bool("version", false, "print the version and exit")
	debug := fs.Bool("debug", false, "verbose logging and debug mode for this session")
	startHidden := fs.Bool("start-hidden", false, "open no window at launch")
	noTray := fs.Bool("no-tray", false, "run without the tray icon")
	inProcess := fs.Bool("engine-in-process", false, "run the speech engine inside the app")
	toggle := fs.Bool(string(instance.Toggle), false, "start or stop dictation in the running app")
	cancel := fs.Bool(string(instance.Cancel), false, "cancel dictation in the running app")

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

	if cmd, msg := remoteCommand(fs, *toggle, *cancel); msg != "" {
		fmt.Fprintf(env.Stderr, "goscriber: %s\n\n%s", msg, usage)
		return 2
	} else if cmd != "" {
		return runRemote(cmd, env)
	}

	if *showVersion {
		fmt.Fprintf(env.Stdout, "goscriber %s (%s %s/%s)\n",
			ResolveVersion(env.Version), runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return 0
	}

	if env.RunApp == nil {
		fmt.Fprintln(env.Stderr, "goscriber: the desktop app is not available in this build; run 'goscriber --help'")
		return 0
	}
	inst, code, done := claimInstance(env)
	if done {
		return code
	}
	defer inst.Close()
	opts := AppOptions{
		Debug:           *debug,
		StartHidden:     *startHidden,
		NoTray:          *noTray,
		EngineInProcess: *inProcess,
		Version:         ResolveVersion(env.Version),
		Instance:        inst,
	}
	if err := env.RunApp(opts); err != nil {
		fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
		return 1
	}
	return 0
}

// remoteCommand returns the command the remote-control flags ask for, or ""
// when none was given. msg explains a usage error: two remote-control
// flags, or one with flags that only mean something to a launch.
func remoteCommand(fs *flag.FlagSet, toggle, cancel bool) (cmd instance.Command, msg string) {
	switch {
	case toggle && cancel:
		return "", "--toggle-transcription and --cancel cannot be combined"
	case toggle:
		cmd = instance.Toggle
	case cancel:
		cmd = instance.Cancel
	default:
		return "", ""
	}
	var others []string
	fs.Visit(func(f *flag.Flag) {
		if f.Name != string(cmd) {
			others = append(others, "--"+f.Name)
		}
	})
	if len(others) > 0 {
		return "", fmt.Sprintf("--%s controls the running app and takes no other flags (%s)", cmd, strings.Join(others, ", "))
	}
	return cmd, ""
}

// runRemote sends cmd to the running app.
func runRemote(cmd instance.Command, env Env) int {
	paths, err := config.ResolvePaths()
	if err != nil {
		fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
		return exitFailure
	}
	ctx, stop := interruptible()
	defer stop()
	switch err := instance.Send(ctx, paths.ConfigDir, cmd); {
	case err == nil:
		return exitOK
	case errors.Is(err, instance.ErrNotRunning):
		fmt.Fprintln(env.Stderr, "goscriber: Goscriber is not running; start it first")
	default:
		fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
	}
	return exitFailure
}

// claimInstance makes this launch the only Goscriber running. When another
// one runs, it asks that one to show its settings window, and done is true
// with the exit code. Where the system has no single-instance support, or
// the lock cannot be taken, the app runs anyway, without remote control.
func claimInstance(env Env) (inst *instance.Instance, code int, done bool) {
	paths, err := config.ResolvePaths()
	if err != nil {
		return nil, 0, false // the app reports it when it starts
	}
	// Each round either takes the lock or reaches the app that holds it,
	// unless that app quits in between; three rounds are plenty.
	for range 3 {
		held, err := instance.Acquire(paths.ConfigDir)
		switch {
		case err == nil:
			return held, 0, false
		case errors.Is(err, instance.ErrUnsupported):
			return nil, 0, false
		case !errors.Is(err, instance.ErrRunning):
			fmt.Fprintf(env.Stderr, "goscriber: cannot make sure only one Goscriber runs: %v\n", err)
			return nil, 0, false
		}
		switch err := instance.Send(context.Background(), paths.ConfigDir, instance.Show); {
		case err == nil:
			fmt.Fprintln(env.Stdout, "Goscriber is already running; its settings window is in front.")
			return nil, exitOK, true
		case !errors.Is(err, instance.ErrNotRunning):
			fmt.Fprintf(env.Stderr, "goscriber: Goscriber is already running: %v\n", err)
			return nil, exitFailure, true
		}
	}
	fmt.Fprintln(env.Stderr, "goscriber: another Goscriber keeps starting and quitting; try again")
	return nil, exitFailure, true
}
