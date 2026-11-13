package app

import (
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/logging"
)

// Options are the startup choices made on the command line.
type Options struct {
	Debug       bool      // --debug: debug logging and debug mode for this session
	StartHidden bool      // --start-hidden: open no window at launch
	NoTray      bool      // --no-tray: no tray icon; the settings window opens instead
	Version     string    // shown in the startup log line
	Stderr      io.Writer // nil means os.Stderr
	// NewEngine returns the speech engine dictation transcribes with. Nil
	// means this build has none: dictation records, then says so.
	NewEngine func() engine.Engine
}

// Runtime is what every part of the app shares: where files live, the
// settings and the logger. Close it on shutdown.
type Runtime struct {
	Paths    config.Paths
	Settings *config.Store
	Log      *logging.Logger
	// DebugMode reveals debug-only UI. It is on when the saved setting is on or
	// when --debug was passed.
	DebugMode bool

	prevDefault *slog.Logger
	unsubscribe func()
	following   sync.WaitGroup
	closeOnce   sync.Once
}

// Start resolves paths, loads the settings and starts logging. The logger
// becomes slog's default, and its level follows the log_level setting unless
// --debug or GOSCRIBER_LOG pins it.
func Start(opts Options) (*Runtime, error) {
	paths, err := config.ResolvePaths()
	if err != nil {
		return nil, err
	}
	store, report, err := config.Open(paths.SettingsFile())
	if err != nil {
		return nil, err
	}
	env := os.Getenv(logging.EnvLevel)
	settings := store.Get()
	log, err := logging.New(logging.Options{
		Dir:    paths.LogsDir(),
		Level:  logging.EffectiveLevel(string(settings.LogLevel), opts.Debug, env),
		Stderr: opts.Stderr,
	})
	if err != nil {
		return nil, err
	}

	rt := &Runtime{
		Paths:       paths,
		Settings:    store,
		Log:         log,
		DebugMode:   settings.DebugMode || opts.Debug,
		prevDefault: slog.Default(),
	}
	slog.SetDefault(log.Logger)

	updates, unsubscribe := store.Subscribe()
	rt.unsubscribe = unsubscribe
	rt.following.Go(func() {
		for s := range updates {
			log.SetLevel(logging.EffectiveLevel(string(s.LogLevel), opts.Debug, env))
		}
	})

	log.Info("goscriber starting",
		"version", opts.Version,
		"log_level", log.Level().String(),
		"debug_mode", rt.DebugMode,
		"settings", store.Path(),
		"log_file", log.FilePath())
	logSettingsReport(log, report)
	return rt, nil
}

// Close stops following settings changes, restores slog's previous default
// logger and closes the log file. It is safe to call more than once.
func (rt *Runtime) Close() error {
	var err error
	rt.closeOnce.Do(func() {
		rt.unsubscribe()
		rt.following.Wait()
		slog.SetDefault(rt.prevDefault)
		err = rt.Log.Close()
	})
	return err
}

func logSettingsReport(log *logging.Logger, r config.LoadReport) {
	switch {
	case r.Created:
		log.Info("no settings file yet; using defaults")
	case r.Reset:
		log.Warn("settings file was unreadable; using defaults", "backup", r.BackupPath)
	case r.Newer:
		log.Warn("settings file is from a newer Goscriber; unknown fields are ignored",
			"file_version", r.FromVersion, "backup", r.BackupPath)
	case r.Migrated:
		log.Info("settings migrated", "from_version", r.FromVersion, "to_version", config.SchemaVersion)
	}
	if len(r.Fixed) > 0 {
		log.Warn("invalid settings replaced with defaults", "fields", r.Fixed)
	}
}
