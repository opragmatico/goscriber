package pipeline

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"time"

	"github.com/opragmatico/goscriber/hook"
	"github.com/opragmatico/goscriber/internal/notice"
)

// HookStage is the transcription hook text stage (#38): it runs the program
// at hooks/transcription in Goscriber's config directory on the transcript
// about to be pasted, and passes on what the program prints instead. The
// contract is Handy's; package hook describes it and runs the program.
//
// The program is looked up for every transcript, so one added, changed or
// removed takes effect at the next dictation; with none, the transcript
// passes unchanged. A blank transcript passes without running it. A program
// that cannot run, fails or outlives hook.DefaultTimeout makes Process fail,
// so the coordinator pastes the transcript the stage was given, and the user
// hears why through a notice. A cancelled session stops the program and
// raises no notice.
type HookStage struct {
	// ConfigDir is Goscriber's config directory, whose hooks/transcription
	// runs.
	ConfigDir string
	// Notices, if set, hears about a hook that failed.
	Notices Notices
	// timeout replaces hook.DefaultTimeout, for tests.
	timeout time.Duration
}

// Process implements TextStage.
func (h HookStage) Process(ctx context.Context, text string, info SessionInfo) (string, error) {
	if strings.TrimSpace(text) == "" {
		return text, nil
	}
	s, err := hook.Find(h.ConfigDir)
	if errors.Is(err, fs.ErrNotExist) {
		return text, nil
	}
	if err == nil {
		s.Timeout = h.timeout
		var out string
		if out, err = s.Rewrite(ctx, text, hook.Info{Model: info.Model, Language: info.TextLanguage}); err == nil {
			return out, nil
		}
	}
	if ctx.Err() == nil && h.Notices != nil {
		h.Notices.Publish(notice.HookFailed(err))
	}
	return "", err
}
