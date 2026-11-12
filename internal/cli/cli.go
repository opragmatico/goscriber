// Package cli parses Goscriber's command line: the desktop app's flags, the
// transcribe, models and debug subcommands, and later engine-worker and the
// remote-control flags that are forwarded to a running instance.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/models"
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
	// Models manages the model files for the subcommands. Nil means one for
	// the user's models directory, which also looks in the Hugging Face
	// cache; tests supply their own.
	Models *models.Manager
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
	Version     string
}

const usage = `Goscriber: offline push-to-talk speech-to-text.

Usage:
  goscriber [flags]            start the desktop app
  goscriber transcribe FILE... transcribe WAV files and print the text
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
  -h, --help           show this help and exit
      --no-tray        run without the tray icon; the settings window opens instead
      --start-hidden   open no window at launch
      --version        print the version and exit

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

	if *showVersion {
		fmt.Fprintf(env.Stdout, "goscriber %s (%s %s/%s)\n",
			ResolveVersion(env.Version), runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return 0
	}

	if env.RunApp == nil {
		fmt.Fprintln(env.Stderr, "goscriber: the desktop app is not available in this build; run 'goscriber --help'")
		return 0
	}
	opts := AppOptions{
		Debug:       *debug,
		StartHidden: *startHidden,
		NoTray:      *noTray,
		Version:     ResolveVersion(env.Version),
	}
	if err := env.RunApp(opts); err != nil {
		fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
		return 1
	}
	return 0
}
