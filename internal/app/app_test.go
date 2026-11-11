package app

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/logging"
)

func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.HomeEnv, home)
	t.Setenv(logging.EnvLevel, "")
	return home
}

func start(t *testing.T, opts Options) *Runtime {
	t.Helper()
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	rt, err := Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	return rt
}

func waitForLevel(t *testing.T, rt *Runtime, want slog.Level) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for rt.Log.Level() != want {
		if time.Now().After(deadline) {
			t.Fatalf("log level is %v, want %v", rt.Log.Level(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStartCreatesLogFileAndUsesSettingsLevel(t *testing.T) {
	isolate(t)
	rt := start(t, Options{Version: "v0.0.1"})

	if rt.Log.Level() != slog.LevelInfo || rt.DebugMode {
		t.Errorf("level %v, debug mode %v; want info and off", rt.Log.Level(), rt.DebugMode)
	}
	if rt.Log.FilePath() == "" || !strings.HasPrefix(rt.Log.FilePath(), rt.Paths.LogsDir()) {
		t.Fatalf("log file %q is not in %s", rt.Log.FilePath(), rt.Paths.LogsDir())
	}
	data, err := os.ReadFile(rt.Log.FilePath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "goscriber starting") || !strings.Contains(string(data), "version=v0.0.1") {
		t.Errorf("startup line missing from log: %q", data)
	}
	if slog.Default().Handler() != rt.Log.Handler() {
		t.Error("the runtime logger should be slog's default")
	}
}

func TestLevelFollowsSettings(t *testing.T) {
	isolate(t)
	rt := start(t, Options{})
	if err := rt.Settings.Update(func(s *config.Settings) { s.LogLevel = config.LogDebug }); err != nil {
		t.Fatal(err)
	}
	waitForLevel(t, rt, slog.LevelDebug)
	if err := rt.Settings.Update(func(s *config.Settings) { s.LogLevel = config.LogError }); err != nil {
		t.Fatal(err)
	}
	waitForLevel(t, rt, slog.LevelError)
}

func TestDebugFlagPinsDebug(t *testing.T) {
	isolate(t)
	rt := start(t, Options{Debug: true})
	if rt.Log.Level() != slog.LevelDebug || !rt.DebugMode {
		t.Fatalf("level %v, debug mode %v; want debug and on", rt.Log.Level(), rt.DebugMode)
	}
	if err := rt.Settings.Update(func(s *config.Settings) { s.LogLevel = config.LogError }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if rt.Log.Level() != slog.LevelDebug {
		t.Errorf("--debug should win over the setting, level is %v", rt.Log.Level())
	}
	if rt.Settings.Get().DebugMode {
		t.Error("--debug must not be saved into the settings")
	}
}

func TestEnvOverridesSetting(t *testing.T) {
	isolate(t)
	t.Setenv(logging.EnvLevel, "warn")
	rt := start(t, Options{})
	if rt.Log.Level() != slog.LevelWarn {
		t.Errorf("level %v, want warn from %s", rt.Log.Level(), logging.EnvLevel)
	}
}

func TestStartReportsRepairedSettings(t *testing.T) {
	home := isolate(t)
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	rt := start(t, Options{Stderr: &stderr})
	if !strings.Contains(stderr.String(), "settings file was unreadable") {
		t.Errorf("repair not logged: %q", stderr.String())
	}
	if rt.Settings.Get().HistoryLimit != config.Defaults().HistoryLimit {
		t.Error("defaults should be in use after a reset")
	}
}

func TestCloseRestoresDefaultLoggerAndIsIdempotent(t *testing.T) {
	isolate(t)
	before := slog.Default()
	rt, err := Start(Options{Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if slog.Default() != before {
		t.Error("Close did not restore the previous default logger")
	}
}
