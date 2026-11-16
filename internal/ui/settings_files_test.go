package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/test"

	"github.com/opragmatico/goscriber/internal/history"
)

// fileCall is one file the History page asked to transcribe; the test
// answers it.
type fileCall struct {
	path     string
	progress func(done, total time.Duration)
	ctx      context.Context
	reply    chan fileReply
}

type fileReply struct {
	e   history.Entry
	err error
}

// withFiles gives hw a TranscribeFile action that hands each file to the
// test. Call it before the window is first shown.
func withFiles(hw *historyWindow) chan *fileCall {
	calls := make(chan *fileCall, 8)
	hw.opts.HistoryActions.TranscribeFile = func(ctx context.Context, path string, progress func(done, total time.Duration)) (history.Entry, error) {
		c := &fileCall{path: path, progress: progress, ctx: ctx, reply: make(chan fileReply, 1)}
		calls <- c
		select {
		case r := <-c.reply:
			return r.e, r.err
		case <-ctx.Done():
			return history.Entry{}, ctx.Err()
		}
	}
	return calls
}

// nextCall runs UI updates until the page hands TranscribeFile a file.
func (hw *historyWindow) nextCall(t *testing.T, calls chan *fileCall) *fileCall {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case c := <-calls:
			return c
		case f := <-hw.q:
			f()
		case <-deadline:
			t.Fatal("no file was handed to TranscribeFile")
			return nil
		}
	}
}

func (hw *historyWindow) fileStatus() string { return hw.history.files.status.Text }

func TestTranscribeFilesOneAfterAnother(t *testing.T) {
	hw := newHistoryWindow(t)
	calls := withFiles(hw)
	hw.show(PageHistory)
	if !strings.Contains(hw.pageText(PageHistory), HistoryFilesHint) || hw.buttons(PageHistory)[LabelChooseFile] == nil {
		t.Fatal("the History page does not offer to transcribe a file")
	}
	f := &hw.history.files
	if !f.bar.Hidden || !f.cancel.Hidden || !f.copy.Hidden {
		t.Error("progress, Cancel or Copy shown with nothing to transcribe")
	}

	hw.TranscribeFiles([]string{"/audio/interview.flac", "/audio/second.wav"})
	first := hw.nextCall(t, calls)
	if first.path != "/audio/interview.flac" {
		t.Fatalf("first file %q", first.path)
	}
	first.progress(2*time.Minute, 10*time.Minute)
	hw.q.until(t, "the progress", func() bool {
		return hw.fileStatus() == "Transcribing interview.flac… 2 min 0 s of 10 min 0 s (1 more waiting)"
	})
	if f.bar.Hidden || f.bar.Value != 0.2 || f.cancel.Hidden {
		t.Errorf("bar hidden %v at %v, Cancel hidden %v", f.bar.Hidden, f.bar.Value, f.cancel.Hidden)
	}

	saved, err := hw.store.Add(context.Background(), history.Record{
		At: time.Now(), Text: "The interview.", RawText: "the interview", Model: parakeetV2,
		Duration: 10 * time.Minute, Source: "interview.flac",
	})
	if err != nil {
		t.Fatal(err)
	}
	first.reply <- fileReply{e: saved}
	second := hw.nextCall(t, calls)
	if second.path != "/audio/second.wav" {
		t.Fatalf("second file %q", second.path)
	}
	hw.q.until(t, "the second file", func() bool { return hw.fileStatus() == "Transcribing second.wav…" })
	hw.q.until(t, "the new entry", func() bool { return len(hw.history.entries) == 1 })
	if text := hw.pageText(PageHistory); !strings.Contains(text, "interview.flac · 10 min 0 s") || !strings.Contains(text, "The interview.") {
		t.Errorf("the file's entry is not listed with its name: %q", text)
	}

	second.reply <- fileReply{err: errors.New("not a WAV or FLAC file")}
	hw.q.until(t, "the failure", func() bool {
		return hw.fileStatus() == "Could not transcribe second.wav: not a WAV or FLAC file"
	})
	if !f.bar.Hidden || !f.cancel.Hidden || !f.copy.Hidden {
		t.Error("progress, Cancel or Copy still shown after a failure")
	}
}

// A transcript the history did not keep can still be copied.
func TestTranscribedFileCanBeCopied(t *testing.T) {
	hw := newHistoryWindow(t)
	calls := withFiles(hw)
	hw.show(PageHistory)
	hw.TranscribeFiles([]string{"/audio/memo.wav"})
	hw.nextCall(t, calls).reply <- fileReply{e: history.Entry{Text: "Buy milk."}}
	hw.q.until(t, "the result", func() bool {
		return strings.HasPrefix(hw.fileStatus(), "Transcribed memo.wav. The history keeps no dictations")
	})
	copyButton := hw.history.files.copy
	if copyButton.Hidden {
		t.Fatal("no Copy for the transcript")
	}
	test.Tap(copyButton)
	if got := hw.app.Clipboard().Content(); got != "Buy milk." {
		t.Errorf("clipboard %q", got)
	}
}

func TestCancelFileTranscription(t *testing.T) {
	hw := newHistoryWindow(t)
	calls := withFiles(hw)
	hw.show(PageHistory)
	hw.TranscribeFiles([]string{"/audio/long.wav", "/audio/next.wav"})
	c := hw.nextCall(t, calls)
	hw.q.drain()
	test.Tap(hw.history.files.cancel)
	select {
	case <-c.ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Cancel did not stop the transcription")
	}
	hw.q.until(t, "the stop", func() bool { return hw.fileStatus() == "Stopped transcribing long.wav." })
	select {
	case c := <-calls:
		t.Errorf("%s was transcribed after Cancel", c.path)
	case <-time.After(100 * time.Millisecond):
	}
}

// Files dropped on the window are transcribed on the History page; other
// URIs, and drops during onboarding, are ignored.
func TestDroppedFilesAreTranscribed(t *testing.T) {
	hw := newHistoryWindow(t)
	calls := withFiles(hw)
	hw.show(PageGeneral)
	web, err := storage.ParseURI("https://example.com/talk.wav")
	if err != nil {
		t.Fatal(err)
	}
	hw.dropped(fyne.NewPos(10, 10), []fyne.URI{storage.NewFileURI("/audio/dropped.wav"), web})
	if hw.Page() != PageHistory {
		t.Errorf("a drop left the window on %v", hw.Page())
	}
	c := hw.nextCall(t, calls)
	if c.path != "/audio/dropped.wav" {
		t.Errorf("transcribing %q", c.path)
	}
	c.reply <- fileReply{e: history.Entry{Text: "ok"}}
	hw.q.until(t, "the result", func() bool { return strings.HasPrefix(hw.fileStatus(), "Transcribed dropped.wav") })

	hw.ShowOnboarding(StepWelcome)
	hw.dropped(fyne.NewPos(10, 10), []fyne.URI{storage.NewFileURI("/audio/during-setup.wav")})
	select {
	case c := <-calls:
		t.Errorf("%s was transcribed during onboarding", c.path)
	case <-time.After(100 * time.Millisecond):
	}
}

// Without a way to transcribe files the page does not offer it.
func TestNoFileTranscriptionWithoutTheAction(t *testing.T) {
	hw := newHistoryWindow(t)
	hw.show(PageHistory)
	if strings.Contains(hw.pageText(PageHistory), HistoryFilesHint) || hw.buttons(PageHistory)[LabelChooseFile] != nil {
		t.Error("the page offers file transcription with no action behind it")
	}
	hw.dropped(fyne.NewPos(0, 0), []fyne.URI{storage.NewFileURI("/audio/x.wav")}) // nothing to do, no panic
}

func TestHistoryHeadingNamesTheFile(t *testing.T) {
	now := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	e := history.Entry{At: now.Add(-time.Minute), Duration: 10 * time.Minute, Source: "interview.flac"}
	if got, want := historyHeading(e, "M", now), "Today 08:59 · interview.flac · 10 min 0 s · M"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
}
