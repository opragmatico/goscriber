package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/history"
)

// historyWindow is a test window over a history in a temporary directory,
// recording what its actions are asked to do.
type historyWindow struct {
	*testWindow
	store *history.Store

	mu     sync.Mutex
	pasted []string
	played []string
}

func newHistoryWindow(t *testing.T) *historyWindow {
	t.Helper()
	dir := t.TempDir()
	store, err := history.Open(filepath.Join(dir, "history.sqlite3"), filepath.Join(dir, "recordings"),
		history.Retention{Limit: 100, Recordings: config.RetentionPreserveLimit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	hw := &historyWindow{store: store}
	hw.testWindow = newTestWindow(t, func(o *SettingsOptions) {
		o.History = store
		o.HistoryActions = HistoryActions{
			Paste: func(text string) {
				hw.mu.Lock()
				defer hw.mu.Unlock()
				hw.pasted = append(hw.pasted, text)
			},
			Play: func(ctx context.Context, path string) error {
				hw.mu.Lock()
				hw.played = append(hw.played, path)
				hw.mu.Unlock()
				<-ctx.Done() // plays until stopped
				return ctx.Err()
			},
			Retranscribe: func(ctx context.Context, e history.Entry) (history.Entry, error) {
				return store.Retranscribed(ctx, e.ID, history.Transcript{Text: "Better: " + e.Text, RawText: e.RawText, Model: nemotron})
			},
			Reprocess: func(ctx context.Context, e history.Entry) (history.Entry, error) {
				p := "Processed: " + e.RawText
				return store.Reprocessed(ctx, e.ID, history.Processing{Text: p, ProcessedText: p})
			},
		}
	})
	return hw
}

func (hw *historyWindow) add(t *testing.T, at time.Time, text string, recording bool) history.Entry {
	t.Helper()
	rec := history.Record{At: at, Text: text, RawText: strings.ToLower(text), Model: parakeetV2, Duration: 6 * time.Second}
	if recording {
		rec.Samples, rec.SampleRate = make([]float32, 1600), 16000
	}
	e, err := hw.store.Add(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// rowButton is the visible button with text on the i-th row (the newest is
// 0), or nil.
func (tw *testWindow) rowButton(t *testing.T, i int, text string) *widget.Button {
	t.Helper()
	rows := tw.history.box.Objects
	if i >= len(rows) {
		t.Fatalf("no row %d of %d", i, len(rows))
	}
	rows[i].Resize(fyne.NewSize(700, 200))
	for _, o := range test.LaidOutObjects(rows[i]) {
		if b, ok := o.(*widget.Button); ok && b.Visible() && b.Text == text {
			return b
		}
	}
	return nil
}

func TestHistoryPage(t *testing.T) {
	now := time.Date(2026, 11, 15, 16, 0, 0, 0, time.Local)
	historyNow = func() time.Time { return now }
	t.Cleanup(func() { historyNow = time.Now })

	hw := newHistoryWindow(t)
	old := hw.add(t, now.Add(-26*time.Hour), "Yesterday's note.", false)
	recent := hw.add(t, now.Add(-time.Hour), "Send the report by Friday.", true)
	hw.show(PageHistory)

	text := hw.pageText(PageHistory)
	for _, want := range []string{
		"Today 15:00 · 6 s · Parakeet TDT 0.6B v2", "Send the report by Friday.",
		"Yesterday 14:00 · 6 s · Parakeet TDT 0.6B v2", "Yesterday's note.", "[" + LabelDeleteAll + "]",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%q missing from the page:\n%s", want, text)
		}
	}
	if strings.Index(text, "Send the report") > strings.Index(text, "Yesterday's note") {
		t.Error("the latest dictation should come first")
	}
	for _, label := range []string{LabelCopy, LabelPasteAgain, LabelPlay, LabelRetranscribe, LabelReprocess, LabelDeleteEntry} {
		if hw.rowButton(t, 0, label) == nil {
			t.Errorf("no [%s] on a dictation with its recording", label)
		}
	}
	if hw.rowButton(t, 1, LabelPlay) != nil || hw.rowButton(t, 1, LabelRetranscribe) != nil {
		t.Error("a dictation without its recording cannot be played or transcribed again")
	}

	test.Tap(hw.rowButton(t, 0, LabelCopy))
	if got := hw.app.Clipboard().Content(); got != recent.Text {
		t.Errorf("clipboard %q", got)
	}
	test.Tap(hw.rowButton(t, 0, LabelPasteAgain))
	if len(hw.pasted) != 1 || hw.pasted[0] != recent.Text {
		t.Errorf("pasted %q", hw.pasted)
	}

	// Play turns into Stop until the recording ends or is stopped.
	test.Tap(hw.rowButton(t, 0, LabelPlay))
	if hw.rowButton(t, 0, LabelStopPlaying) == nil {
		t.Fatal("no Stop while playing")
	}
	test.Tap(hw.rowButton(t, 0, LabelStopPlaying))
	hw.q.drain()
	if hw.rowButton(t, 0, LabelPlay) == nil || len(hw.played) != 1 || hw.played[0] != recent.Recording {
		t.Errorf("after Stop: played %v", hw.played)
	}

	test.Tap(hw.rowButton(t, 0, LabelRetranscribe))
	if !strings.Contains(hw.pageText(PageHistory), HistoryRetranscribing) || !hw.rowButton(t, 0, LabelRetranscribe).Disabled() {
		t.Error("the row should say it is being transcribed again")
	}
	hw.q.until(t, "the new transcript", func() bool { return strings.Contains(hw.pageText(PageHistory), "Better: Send the report") })
	if got := hw.pageText(PageHistory); !strings.Contains(got, "Nemotron Streaming 3.5") || strings.Contains(got, HistoryRetranscribing) {
		t.Errorf("after transcribing again:\n%s", got)
	}

	test.Tap(hw.rowButton(t, 1, LabelDeleteEntry))
	hw.q.until(t, "the deletion", func() bool { return !strings.Contains(hw.pageText(PageHistory), old.Text) })

	// A dictation saved while the window is open shows up.
	hw.add(t, now, "Fresh.", false)
	hw.RefreshHistory()
	hw.q.until(t, "the new dictation", func() bool { return strings.Contains(hw.pageText(PageHistory), "Fresh.") })

	var confirmed bool
	hw.confirm = func(_, _, _ string, ok func()) { confirmed = true; ok() }
	test.Tap(hw.buttons(PageHistory)[LabelDeleteAll])
	hw.q.until(t, "the empty history", func() bool { return strings.Contains(hw.pageText(PageHistory), HistoryEmpty) })
	if !confirmed {
		t.Error("Delete all should ask first")
	}
	if _, err := os.Stat(recent.Recording); !errors.Is(err, os.ErrNotExist) {
		t.Error("Delete all left a recording")
	}
	if b := hw.buttons(PageHistory)[LabelDeleteAll]; b != nil {
		t.Error("nothing left to delete")
	}
}

// A failed re-transcription says why on its row.
func TestHistoryRetranscribeFailure(t *testing.T) {
	hw := newHistoryWindow(t)
	hw.opts.HistoryActions.Retranscribe = func(context.Context, history.Entry) (history.Entry, error) {
		return history.Entry{}, errors.New("no speech in the recording")
	}
	hw.add(t, time.Now(), "Hello.", true)
	hw.show(PageHistory)
	test.Tap(hw.rowButton(t, 0, LabelRetranscribe))
	hw.q.until(t, "the failure", func() bool {
		return strings.Contains(hw.pageText(PageHistory), "Could not transcribe it again: no speech in the recording")
	})
	if hw.rowButton(t, 0, LabelRetranscribe).Disabled() {
		t.Error("the user can try again")
	}
}

// Process again post-processes an entry's transcript, recording or not,
// and shows the new text, marked as post-processed; a failure says why and
// keeps the text.
func TestHistoryReprocess(t *testing.T) {
	hw := newHistoryWindow(t)
	hw.add(t, time.Now().Add(-time.Minute), "Hello there.", false)
	hw.show(PageHistory)
	if strings.Contains(hw.pageText(PageHistory), "post-processed") {
		t.Error("an entry that was not post-processed says it was")
	}

	test.Tap(hw.rowButton(t, 0, LabelReprocess))
	if !strings.Contains(hw.pageText(PageHistory), HistoryReprocessing) || !hw.rowButton(t, 0, LabelReprocess).Disabled() {
		t.Error("the row should say it is being post-processed again")
	}
	hw.q.until(t, "the new text", func() bool { return strings.Contains(hw.pageText(PageHistory), "Processed: hello there.") })
	if got := hw.pageText(PageHistory); !strings.Contains(got, "post-processed") || strings.Contains(got, HistoryReprocessing) {
		t.Errorf("after post-processing again:\n%s", got)
	}

	hw.opts.HistoryActions.Reprocess = func(context.Context, history.Entry) (history.Entry, error) {
		return history.Entry{}, errors.New("provider down")
	}
	hw.RefreshHistory()
	hw.q.drain()
	test.Tap(hw.rowButton(t, 0, LabelReprocess))
	hw.q.until(t, "the failure", func() bool {
		return strings.Contains(hw.pageText(PageHistory), "Could not post-process it again: provider down")
	})
	if !strings.Contains(hw.pageText(PageHistory), "Processed: hello there.") || hw.rowButton(t, 0, LabelReprocess).Disabled() {
		t.Error("a failure keeps the text, and the user can try again")
	}

	// An entry with no transcript has nothing to process.
	if _, err := hw.store.Add(context.Background(), history.Record{At: time.Now(), Model: parakeetV2, Duration: time.Second}); err != nil {
		t.Fatal(err)
	}
	hw.RefreshHistory()
	hw.q.until(t, "the empty entry", func() bool { return strings.Contains(hw.pageText(PageHistory), historyNothingDelivered) })
	if hw.rowButton(t, 0, LabelReprocess) != nil {
		t.Error("Process again on an entry with no transcript")
	}
}

func TestHistoryHeading(t *testing.T) {
	now := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		at   time.Time
		dur  time.Duration
		want string
	}{
		{now.Add(-time.Minute), 4 * time.Second, "Today 08:59 · 4 s · M"},
		{now.Add(-10 * time.Hour), 75 * time.Second, "Yesterday 23:00 · 1 min 15 s · M"},
		{time.Date(2026, 3, 2, 14, 5, 0, 0, time.UTC), 0, "2 Mar 14:05 · 0 s · M"},
		{time.Date(2025, 12, 31, 7, 0, 0, 0, time.UTC), time.Second, "31 Dec 2025 07:00 · 1 s · M"},
	} {
		if got := historyHeading(history.Entry{At: tt.at, Duration: tt.dur}, "M", now); got != tt.want {
			t.Errorf("%v: %q, want %q", tt.at, got, tt.want)
		}
	}
	processed := history.Entry{At: now.Add(-time.Minute), Duration: time.Second, ProcessedText: "Fixed."}
	if got, want := historyHeading(processed, "M", now), "Today 08:59 · 1 s · M · post-processed"; got != want {
		t.Errorf("post-processed: %q, want %q", got, want)
	}
}

// While a webhook is set, each entry shows its number, the transcription_id
// the webhook got.
func TestHistoryShowsEntryNumbersWithAWebhook(t *testing.T) {
	hw := newHistoryWindow(t)
	e := hw.add(t, time.Now(), "Numbered.", false)
	hw.show(PageHistory)
	number := fmt.Sprintf(" · #%d", e.ID)
	if strings.Contains(hw.pageText(PageHistory), number) {
		t.Error("an entry shows its number without a webhook")
	}
	if err := hw.testWindow.store.Update(func(s *config.Settings) { s.WebhookURL = "https://hooks.example.com/x" }); err != nil {
		t.Fatal(err)
	}
	hw.SetSettings(hw.testWindow.store.Get())
	if got := hw.pageText(PageHistory); !strings.Contains(got, number) {
		t.Errorf("no %q with a webhook:\n%s", number, got)
	}
}
