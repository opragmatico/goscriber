// Package hook is Goscriber's transcription hook: a program that rewrites the
// transcript right before Goscriber pastes it, under the contract Handy set
// for its own hook.
//
// # The contract
//
// The hook is the executable at hooks/transcription in Goscriber's config
// directory (see Path): ~/Library/Application Support/goscriber on macOS,
// ~/.config/goscriber on Linux, or $GOSCRIBER_HOME when it is set. Goscriber
// looks for it before every paste, so adding, changing or removing it takes
// effect at the next dictation.
//
//   - The transcript, after text cleanup, arrives on standard input, and what
//     the program prints on standard output is pasted instead.
//   - One trailing newline is removed from the output, so the output of echo
//     pastes no line break, and empty output pastes nothing.
//   - When the program cannot start, exits with a status other than 0 or runs
//     longer than 30 seconds (DefaultTimeout), the original transcript is
//     pasted. A program still running is stopped, with whatever it started:
//     SIGTERM, then SIGKILL a second later. Cancelling the dictation stops it
//     too, and nothing is pasted.
//   - It inherits Goscriber's environment, with GOSCRIBER_MODEL and
//     GOSCRIBER_LANGUAGE added (see Info). That environment is not a login
//     shell's: an app opened from the Finder, the Dock or at login on macOS
//     has launchd's PATH, /usr/bin:/bin:/usr/sbin:/sbin, and a Linux desktop
//     session's PATH lacks what an interactive shell's startup files add. The
//     program should name anything else by its full path, such as
//     /opt/homebrew/bin/jq. Being run directly, a script needs a #! line.
//
// # Both ends
//
// The package holds both ends of the contract, so the hook is also how
// Goscriber becomes one step of a larger program, such as a workflow:
//
//   - Script runs a hook program, as Goscriber does before pasting. A workflow
//     can run the same program as one of its own steps.
//   - Main makes a Go program a hook. The program can hand the transcript to a
//     workflow or an agent and print what comes back, which Goscriber pastes.
//
// Hook is what both rewrite with, so a rewrite written once runs in either
// place: Script implements it, and Func makes one from a function.
package hook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
)

// Name is the hook program's file name, in the hooks directory of
// Goscriber's config directory.
const Name = "transcription"

// DefaultTimeout is how long a hook program may run before it is killed and
// the original transcript is kept: Handy's 30 seconds.
const DefaultTimeout = 30 * time.Second

// MaxOutput is the most a hook program may print, in bytes. A program that
// prints more fails: no transcript comes near it, so the program has gone
// wrong.
const MaxOutput = 1 << 20

// The environment variables that carry Info to a hook program, on top of the
// environment it inherits. Each is set, empty when its value is not known.
const (
	// EnvModel holds Info.Model.
	EnvModel = "GOSCRIBER_MODEL"
	// EnvLanguage holds Info.Language.
	EnvLanguage = "GOSCRIBER_LANGUAGE"
)

// Info describes the transcript a hook rewrites.
type Info struct {
	// Model is the catalog ID of the model that transcribed it, such as
	// "parakeet-tdt-0.6b-v2".
	Model string `json:"model,omitempty"`
	// Language is the transcript's language as best known: the one the model
	// detected, else the one it was asked for, else the model's only
	// language, such as "es-US" or "en". It is "" when not known.
	Language string `json:"language,omitempty"`
}

// environ is the Info as a hook program's environment variables.
func (i Info) environ() []string {
	return []string{EnvModel + "=" + i.Model, EnvLanguage + "=" + i.Language}
}

// infoFromEnv is the Info a hook program was run with.
func infoFromEnv(getenv func(string) string) Info {
	return Info{Model: getenv(EnvModel), Language: getenv(EnvLanguage)}
}

// Hook rewrites a transcript before it is used.
//
// Rewrite returns the text to use instead, which may be empty to use
// nothing. An error means the hook failed, and Goscriber then pastes the
// original transcript; callers that want Goscriber's behaviour do the same.
// Rewrite returns once ctx is done.
type Hook interface {
	Rewrite(ctx context.Context, text string, info Info) (string, error)
}

// Func adapts a function to a Hook.
type Func func(ctx context.Context, text string, info Info) (string, error)

// Rewrite implements Hook.
func (f Func) Rewrite(ctx context.Context, text string, info Info) (string, error) {
	return f(ctx, text, info)
}

// Path is where the hook program lives, given Goscriber's config directory:
// configDir/hooks/transcription.
func Path(configDir string) string { return filepath.Join(configDir, "hooks", Name) }

// ErrNotExecutable means the hook program is a file without execute
// permission, which chmod +x gives it.
var ErrNotExecutable = errors.New("not executable")

// Find returns the hook in Goscriber's config directory configDir, as a
// Script with the default timeout. A symbolic link is followed.
//
// The error matches fs.ErrNotExist when there is no hook. Any other error
// means there is one that cannot run: something other than a file, or a file
// without execute permission (ErrNotExecutable; Windows has no such
// permission, so it is not checked there).
func Find(configDir string) (Script, error) {
	p := Path(configDir)
	fi, err := os.Stat(p)
	switch {
	case err != nil:
		return Script{}, fmt.Errorf("hook: %w", err)
	case !fi.Mode().IsRegular():
		return Script{}, fmt.Errorf("hook %s: not a file", p)
	case runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0:
		return Script{}, fmt.Errorf("hook %s: %w", p, ErrNotExecutable)
	}
	return Script{Path: p}, nil
}

// Default is Find in the config directory Goscriber itself uses:
// $GOSCRIBER_HOME when it is set, else goscriber in os.UserConfigDir. It is
// the hook Goscriber runs before pasting, for a program that wants to rewrite
// its own transcripts the same way.
func Default() (Script, error) {
	paths, err := config.ResolvePaths()
	if err != nil {
		return Script{}, fmt.Errorf("hook: %w", err)
	}
	return Find(paths.ConfigDir)
}
