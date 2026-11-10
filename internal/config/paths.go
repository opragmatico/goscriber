package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// HomeEnv names the environment variable that, when set, puts every Goscriber
// file under one directory. Tests and portable installs use it.
const HomeEnv = "GOSCRIBER_HOME"

const appDirName = "goscriber"

// Paths says where Goscriber keeps its files. ConfigDir holds what the user
// would back up (settings, history, recordings); CacheDir holds what can be
// downloaded or regenerated (models, logs).
type Paths struct {
	ConfigDir string
	CacheDir  string
}

// ResolvePaths returns the directories from HomeEnv when it is set, and
// otherwise from os.UserConfigDir and os.UserCacheDir. It does not create them.
func ResolvePaths() (Paths, error) {
	if home := os.Getenv(HomeEnv); home != "" {
		abs, err := filepath.Abs(home)
		if err != nil {
			return Paths{}, fmt.Errorf("config: resolve %s: %w", HomeEnv, err)
		}
		return Paths{ConfigDir: abs, CacheDir: abs}, nil
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, fmt.Errorf("config: user config dir: %w", err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return Paths{}, fmt.Errorf("config: user cache dir: %w", err)
	}
	return Paths{
		ConfigDir: filepath.Join(cfg, appDirName),
		CacheDir:  filepath.Join(cache, appDirName),
	}, nil
}

// SettingsFile is the JSON settings file.
func (p Paths) SettingsFile() string { return filepath.Join(p.ConfigDir, "settings.json") }

// HistoryDB is the SQLite transcription history.
func (p Paths) HistoryDB() string { return filepath.Join(p.ConfigDir, "history.sqlite3") }

// RecordingsDir holds the WAV recordings referenced by the history.
func (p Paths) RecordingsDir() string { return filepath.Join(p.ConfigDir, "recordings") }

// ModelsDir holds downloaded model files.
func (p Paths) ModelsDir() string { return filepath.Join(p.CacheDir, "models") }

// LogsDir holds the rotating log files.
func (p Paths) LogsDir() string { return filepath.Join(p.CacheDir, "logs") }

// EnsureDir creates dir and its parents, readable only by the current user,
// if they do not exist yet.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: create %s: %w", dir, err)
	}
	return nil
}
