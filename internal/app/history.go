package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/history"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/output"
	"github.com/opragmatico/goscriber/internal/pipeline"
	"github.com/opragmatico/goscriber/internal/ui"
)

// recordingPlayer plays a saved recording. *audio.Player implements it.
type recordingPlayer interface {
	Play(ctx context.Context, s audio.Sound, device string, volume float64) error
}

// pasteAgainDelay is how long Paste again waits, after hiding the settings
// window, for the app the user was in to take the keyboard back.
var pasteAgainDelay = 300 * time.Millisecond

// openHistory opens the history in the config directory. Without it the
// app runs on, saving no dictations, and the History page says so.
func openHistory(rt *Runtime) *history.Store {
	s, err := history.Open(rt.Paths.HistoryDB(), rt.Paths.RecordingsDir(), history.RetentionFrom(rt.Settings.Get()))
	if err != nil {
		rt.Log.Warn("the history could not be opened; dictations are not saved", "err", err)
		return nil
	}
	return s
}

// historySink saves each finished dictation to the history and shows it on
// the History page if the window is open.
func (sh *shell) historySink() pipeline.HistorySink {
	if sh.history == nil {
		return nil
	}
	return pipeline.HistorySinkFunc(func(ctx context.Context, e pipeline.HistoryEntry) error {
		_, err := sh.history.Add(ctx, history.Record{
			At:         e.At,
			Text:       e.Text,
			RawText:    e.RawText,
			Model:      e.Model,
			Language:   e.TextLanguage,
			Duration:   e.Duration,
			Samples:    e.Samples,
			SampleRate: e.SampleRate,
		})
		sh.post(sh.settings.RefreshHistory)
		return err
	})
}

// historyStore is the history for the settings window, or nil (not a nil
// *history.Store in an interface) when it could not be opened.
func (sh *shell) historyStore() ui.HistoryStore {
	if sh.history == nil {
		return nil
	}
	return sh.history
}

// historyActions back the History page's buttons.
func (sh *shell) historyActions() ui.HistoryActions {
	if sh.history == nil {
		return ui.HistoryActions{}
	}
	return ui.HistoryActions{
		Paste:        sh.pasteAgain,
		Play:         sh.playRecording,
		Retranscribe: sh.retranscribe,
	}
}

// followHistory applies a change of history_limit or recording_retention at
// once, in the background, pruning what is no longer kept.
func (sh *shell) followHistory(s config.Settings) {
	if sh.history == nil {
		return
	}
	r := history.RetentionFrom(s)
	if r == sh.history.Retention() {
		return
	}
	sh.following.Go(func() {
		if err := sh.history.SetRetention(context.Background(), r); err != nil {
			sh.rt.Log.Warn("the history could not be pruned", "err", err)
		}
		sh.post(sh.settings.RefreshHistory)
	})
}

// pasteAgain delivers a past transcript the way a dictation would. The
// settings window, where the button is, has the keyboard, so it is hidden
// first and the app the user was in gets it back; without a tray it comes
// back once the text is out, as the only way into the app.
func (sh *shell) pasteAgain(text string) {
	sh.settings.Hide()
	s := sh.rt.Settings.Get()
	go func() {
		time.Sleep(pasteAgainDelay)
		err := sh.svc.output.Deliver(context.Background(), text, output.OptionsFrom(s))
		if err != nil {
			sh.rt.Log.Warn("a past transcript could not be pasted again", "err", err)
			sh.notices.Publish(notice.ForPasteError(err))
		}
		if !sh.plan.Tray {
			sh.post(func() { sh.settings.Show(ui.PageHistory) })
		}
	}()
}

// playRecording plays a saved recording on the output device the settings
// name, at full volume.
func (sh *shell) playRecording(ctx context.Context, path string) error {
	samples, rate, err := audio.ReadWAVFile(path)
	if err != nil {
		return err
	}
	return sh.svc.player.Play(ctx, audio.Sound{Samples: samples, Rate: rate}, sh.rt.Settings.Get().OutputDevice, 1)
}

// retranscribe transcribes a saved recording again with the current model
// and saves the new transcript.
func (sh *shell) retranscribe(ctx context.Context, e history.Entry) (history.Entry, error) {
	samples, rate, err := audio.ReadWAVFile(e.Recording)
	if err != nil {
		return history.Entry{}, fmt.Errorf("the recording could not be read: %w", err)
	}
	t, err := sh.dictation.Retranscribe(ctx, samples, rate)
	switch {
	case errors.Is(err, pipeline.ErrNoSpeech):
		return history.Entry{}, errors.New("no speech was found in the recording")
	case errors.Is(err, pipeline.ErrNoModel):
		return history.Entry{}, errors.New("no model is downloaded")
	case err != nil:
		return history.Entry{}, err
	}
	return sh.history.Retranscribed(ctx, e.ID, history.Transcript{Text: t.Text, RawText: t.RawText, Model: t.Model, Language: t.Language})
}

// post runs f on the main thread, unless the app is stopping.
func (sh *shell) post(f func()) {
	select {
	case <-sh.quit:
	default:
		sh.do(f)
	}
}
