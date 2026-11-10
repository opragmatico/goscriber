package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// Defaults for Options fields left at zero.
const (
	FileName           = "goscriber.log"
	DefaultMaxFileSize = 5 << 20 // bytes before the file is rotated
	DefaultMaxFiles    = 3       // goscriber.log plus two rotated files
	DefaultRingSize    = 2000    // records kept in memory for the log viewer
)

// Options configures New.
type Options struct {
	// Dir is the directory for the log file; empty disables the file.
	Dir string
	// Level is the initial minimum level; change it later with SetLevel.
	Level slog.Level
	// Stderr receives a human-readable copy of every record. Nil means
	// os.Stderr; use io.Discard to silence it.
	Stderr io.Writer
	// MaxFileSize, MaxFiles and RingSize default to the package constants.
	MaxFileSize int64
	MaxFiles    int
	RingSize    int
}

// Logger is a slog.Logger that writes to stderr, a rotating file and an
// in-memory ring, all filtered by one level that can change at runtime.
type Logger struct {
	*slog.Logger
	level *slog.LevelVar
	ring  *Ring
	file  *rotatingFile
}

// New builds a Logger. The log directory is created if needed.
func New(opts Options) (*Logger, error) {
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.MaxFileSize <= 0 {
		opts.MaxFileSize = DefaultMaxFileSize
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = DefaultMaxFiles
	}
	if opts.RingSize <= 0 {
		opts.RingSize = DefaultRingSize
	}

	level := new(slog.LevelVar)
	level.Set(opts.Level)
	hopts := &slog.HandlerOptions{Level: level}

	l := &Logger{level: level, ring: NewRing(opts.RingSize)}
	handlers := []slog.Handler{
		slog.NewTextHandler(opts.Stderr, hopts),
		l.ring.Handler(hopts),
	}
	if opts.Dir != "" {
		if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
			return nil, fmt.Errorf("logging: create log dir: %w", err)
		}
		f, err := openRotating(filepath.Join(opts.Dir, FileName), opts.MaxFileSize, opts.MaxFiles)
		if err != nil {
			return nil, err
		}
		l.file = f
		handlers = append(handlers, slog.NewTextHandler(f, hopts))
	}
	l.Logger = slog.New(slog.NewMultiHandler(handlers...))
	return l, nil
}

// SetLevel changes the minimum level of every output at once.
func (l *Logger) SetLevel(level slog.Level) { l.level.Set(level) }

// Level is the current minimum level.
func (l *Logger) Level() slog.Level { return l.level.Level() }

// Ring holds the most recent records for the in-app log viewer.
func (l *Logger) Ring() *Ring { return l.ring }

// FilePath is the current log file, or "" when there is none.
func (l *Logger) FilePath() string {
	if l.file == nil {
		return ""
	}
	return l.file.path
}

// Close closes the log file. Records logged afterwards still reach stderr and
// the ring.
func (l *Logger) Close() error {
	if l.file == nil {
		return nil
	}
	return l.file.Close()
}

// EnvLevel names the environment variable that overrides the configured level.
const EnvLevel = "GOSCRIBER_LOG"

// EffectiveLevel picks the level to use: --debug wins, then a valid
// GOSCRIBER_LOG value, then a valid level from the settings, then info.
func EffectiveLevel(setting string, debugFlag bool, env string) slog.Level {
	if debugFlag {
		return slog.LevelDebug
	}
	if l, err := ParseLevel(env); err == nil {
		return l
	}
	if l, err := ParseLevel(setting); err == nil {
		return l
	}
	return slog.LevelInfo
}

// ParseLevel parses "debug", "info", "warn" or "error" (any case).
func ParseLevel(s string) (slog.Level, error) {
	if s == "" {
		return 0, errors.New("logging: empty level")
	}
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("logging: %w", err)
	}
	return l, nil
}
