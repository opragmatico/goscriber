package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/history"
)

// HistoryStore lists and deletes past dictations for the History page.
// *history.Store implements it.
type HistoryStore interface {
	List(ctx context.Context, limit int) ([]history.Entry, error)
	Count(ctx context.Context) (int, error)
	Delete(ctx context.Context, id int64) error
	DeleteAll(ctx context.Context) error
}

// HistoryActions carry out the History page's buttons on an entry, in the
// background. A nil action leaves its button out; Copy needs none.
type HistoryActions struct {
	// Paste delivers text to the app the user was in, as a dictation does.
	Paste func(text string)
	// Play plays a recording and returns once it has been heard, or ctx is
	// cancelled.
	Play func(ctx context.Context, path string) error
	// Retranscribe transcribes an entry's recording again with the current
	// model, saves the new transcript and returns the entry as it is now.
	Retranscribe func(ctx context.Context, e history.Entry) (history.Entry, error)
	// TranscribeFile transcribes an audio file with the current model,
	// telling progress how far it has got as it goes, saves the transcript
	// to the history and returns its entry. The entry has no ID when the
	// history keeps no dictations; its Text is the transcript either way,
	// also alongside an error in saving it. Nil leaves file transcription
	// out of the page.
	TranscribeFile func(ctx context.Context, path string, progress func(FileProgress)) (history.Entry, error)
}

// FileProgress is how far the transcription of a file has got.
type FileProgress struct {
	// Done and Total are how much of the audio is decoded, of how much.
	Done, Total time.Duration
	// Parts is how many parts the language model cleans the transcript in
	// once the audio is decoded, and Part how many of them it has cleaned;
	// both are 0 until it starts, and without post-processing.
	Part, Parts int
}

// Labels and texts of the History page that tests look for.
const (
	HistoryEmpty            = "No dictations yet. Each one you make is listed here, the latest first."
	HistoryUnavailable      = "Dictations are not being saved: the history could not be opened. The log says why."
	LabelCopy               = "Copy"
	LabelPasteAgain         = "Paste again"
	LabelPlay               = "Play"
	LabelStopPlaying        = "Stop"
	LabelRetranscribe       = "Transcribe again"
	LabelDeleteEntry        = "Delete"
	LabelDeleteAll          = "Delete all"
	HistoryRetranscribing   = "Transcribing again with the current model…"
	historyNothingDelivered = "Nothing was pasted."
	LabelChooseFile         = "Choose a file…"
	LabelCancelFile         = "Cancel"
	HistoryFilesHint        = "Drop WAV or FLAC files on this window, or choose one. Each is transcribed with the selected model, however long it is, and listed with the dictations."
)

// historyShown is the most dictations the page lists.
const historyShown = 100

// historyNow is the clock the page dates entries by; tests replace it.
var historyNow = time.Now

// historyState is what the History page knows about the history.
type historyState struct {
	note      *widget.Label
	deleteAll *widget.Button
	box       *fyne.Container

	entries []history.Entry
	total   int
	err     error
	listed  bool
	listing bool // a read is in progress
	again   bool // read again once it ends

	playing  int64 // the entry whose recording plays, or 0
	stopPlay context.CancelFunc
	busy     map[int64]string // what each entry is waiting for, such as a re-transcription

	files fileJobs
}

// fileJobs are the audio files the History page transcribes, one at a time.
type fileJobs struct {
	// box is the section, and page the History page: both are laid out
	// again as the section's rows come and go.
	box    *fyne.Container
	page   fyne.CanvasObject
	status *widget.Label
	bar    *widget.ProgressBar
	cancel *widget.Button
	copy   *widget.Button

	queue    []string // waiting their turn
	now      string   // the file being transcribed, or ""
	stop     context.CancelFunc
	progress FileProgress // how far it has got
	result   string       // what became of the last file
	text     string       // the last file's transcript, for Copy
}

func (w *SettingsWindow) historyPage() fyne.CanvasObject {
	h := &w.history
	h.busy = map[int64]string{}
	h.note = paragraph("")
	h.deleteAll = widget.NewButtonWithIcon(LabelDeleteAll, theme.DeleteIcon(), w.askDeleteAllHistory)
	h.box = container.NewVBox()
	w.refreshHistoryRows()
	h.files.page = settingsPage("History",
		group("Keep",
			w.row("Dictations", "How many recent dictations to keep. 0 keeps none.",
				intEntrySetting(w, "history_limit", func(s *config.Settings) *int { return &s.HistoryLimit }, 0, 100000)),
			w.row("Recordings", "How long to keep the audio of each dictation.",
				choiceSetting(w, "recording_retention", func(s *config.Settings) *config.RecordingRetention { return &s.RecordingRetention },
					[]choice[config.RecordingRetention]{
						{config.RetentionPreserveLimit, "As long as the dictation is kept"},
						{config.RetentionDays3, "3 days"},
						{config.RetentionWeeks2, "2 weeks"},
						{config.RetentionMonths3, "3 months"},
						{config.RetentionNever, "Do not keep recordings"},
					}, nil)),
		),
		w.fileSection(),
		section("Recent dictations", container.NewBorder(nil, nil, nil, h.deleteAll, h.note), h.box),
	)
	return h.files.page
}

// fileSection lets audio files be transcribed: chosen, or dropped on the
// window. Without a way to transcribe them it is empty.
func (w *SettingsWindow) fileSection() fyne.CanvasObject {
	if w.opts.HistoryActions.TranscribeFile == nil {
		return layout.NewSpacer()
	}
	f := &w.history.files
	f.status = paragraph("")
	f.bar = widget.NewProgressBar()
	f.cancel = widget.NewButtonWithIcon(LabelCancelFile, theme.CancelIcon(), w.cancelFiles)
	f.copy = widget.NewButtonWithIcon(LabelCopy, theme.ContentCopyIcon(), func() { w.app.Clipboard().SetContent(f.text) })
	choose := widget.NewButtonWithIcon(LabelChooseFile, theme.FolderOpenIcon(), w.chooseFile)
	f.box = section("Transcribe a file",
		container.NewBorder(nil, nil, nil, choose, paragraph(HistoryFilesHint)),
		f.status, f.bar, container.NewHBox(f.cancel, f.copy)).(*fyne.Container)
	w.refreshFiles()
	return f.box
}

// chooseFile asks for an audio file to transcribe.
func (w *SettingsWindow) chooseFile() {
	d := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil || r == nil {
			return
		}
		path := r.URI().Path()
		_ = r.Close()
		w.TranscribeFiles([]string{path})
	}, w.win)
	d.SetFilter(storage.NewExtensionFileFilter(audio.FileExtensions))
	d.Show()
}

// dropped transcribes the files dropped on the window, on the History page.
// Drops during onboarding are ignored.
func (w *SettingsWindow) dropped(_ fyne.Position, uris []fyne.URI) {
	if w.opts.HistoryActions.TranscribeFile == nil || w.onboarding.active {
		return
	}
	var paths []string
	for _, u := range uris {
		if u.Scheme() == "file" {
			paths = append(paths, u.Path())
		}
	}
	if len(paths) == 0 {
		return
	}
	if w.Page() != PageHistory {
		w.selectPage(PageHistory)
	}
	w.TranscribeFiles(paths)
}

// TranscribeFiles transcribes audio files one after another, showing their
// progress on the History page and their transcripts in the history.
func (w *SettingsWindow) TranscribeFiles(paths []string) {
	if w.opts.HistoryActions.TranscribeFile == nil || len(paths) == 0 {
		return
	}
	f := &w.history.files
	f.queue = append(f.queue, paths...)
	if f.now == "" {
		w.nextFile()
	}
	w.refreshFiles()
}

// nextFile starts on the next file waiting, if any.
func (w *SettingsWindow) nextFile() {
	f := &w.history.files
	if len(f.queue) == 0 {
		return
	}
	path := f.queue[0]
	f.queue = f.queue[1:]
	ctx, cancel := context.WithCancel(context.Background())
	f.now, f.stop, f.progress = path, cancel, FileProgress{}
	transcribe := w.opts.HistoryActions.TranscribeFile
	go func() {
		e, err := transcribe(ctx, path, func(p FileProgress) {
			w.do(func() {
				if f.now == path {
					f.progress = p
					w.refreshFiles()
				}
			})
		})
		cancelled := ctx.Err() != nil
		cancel()
		w.do(func() { w.fileDone(path, e, err, cancelled) })
	}()
}

// fileDone says what became of a file and moves on to the next.
func (w *SettingsWindow) fileDone(path string, e history.Entry, err error, cancelled bool) {
	f := &w.history.files
	name := filepath.Base(path)
	f.text = e.Text
	switch {
	case cancelled:
		f.result, f.text = "Stopped transcribing "+name+".", ""
	case err != nil:
		w.log().Warn("an audio file could not be transcribed", "file", name, "err", err)
		f.result = fmt.Sprintf("Could not transcribe %s: %v", name, err)
	case e.ID == 0:
		f.result = "Transcribed " + name + ". The history keeps no dictations, so copy the text now."
	default:
		f.result = "Transcribed " + name + ": it is the first of the recent dictations."
	}
	f.now, f.stop = "", nil
	w.nextFile()
	w.refreshFiles()
	w.refreshHistory()
}

// cancelFiles stops the file being transcribed and drops those waiting.
func (w *SettingsWindow) cancelFiles() {
	f := &w.history.files
	f.queue = nil
	if f.stop != nil {
		f.stop()
	}
	w.refreshFiles()
}

// refreshFiles shows the file being transcribed, or what became of the last
// one.
func (w *SettingsWindow) refreshFiles() {
	f := &w.history.files
	if f.status == nil {
		return
	}
	busy := f.now != ""
	p := f.progress
	switch {
	case busy && p.Parts > 0:
		f.status.SetText(fmt.Sprintf("Cleaning up %s with the language model… part %d of %d", filepath.Base(f.now), min(p.Part+1, p.Parts), p.Parts))
	case busy && p.Total > 0:
		f.status.SetText(fmt.Sprintf("Transcribing %s… %s of %s", filepath.Base(f.now), formatLength(p.Done), formatLength(p.Total)))
	case busy:
		f.status.SetText("Transcribing " + filepath.Base(f.now) + "…")
	default:
		f.status.SetText(f.result)
	}
	if n := len(f.queue); busy && n > 0 {
		f.status.SetText(fmt.Sprintf("%s (%d more waiting)", f.status.Text, n))
	}
	value := 0.0
	switch {
	case busy && p.Parts > 0:
		value = float64(p.Part) / float64(p.Parts)
	case busy && p.Total > 0:
		value = float64(p.Done) / float64(p.Total)
	}
	f.bar.SetValue(value)
	showIf(f.status, f.status.Text != "")
	showIf(f.bar, busy)
	showIf(f.cancel, busy)
	showIf(f.copy, !busy && f.text != "")
	f.box.Refresh()
	if f.page != nil {
		f.page.Refresh()
	}
}

// showIf shows o when on holds, and hides it otherwise.
func showIf(o fyne.CanvasObject, on bool) {
	if on {
		o.Show()
	} else {
		o.Hide()
	}
}

// RefreshHistory reads the history again for the History page, when the
// window is open: the app calls it after a dictation is saved or the
// history is pruned. Each Show reads it too.
func (w *SettingsWindow) RefreshHistory() {
	if w.visible {
		w.refreshHistory()
	}
}

// refreshHistory reads the latest entries in the background.
func (w *SettingsWindow) refreshHistory() {
	h := &w.history
	store := w.opts.History
	if store == nil || h.box == nil {
		h.listed = true
		w.refreshHistoryRows()
		return
	}
	if h.listing {
		h.again = true
		return
	}
	h.listing = true
	go func() {
		ctx := context.Background()
		entries, err := store.List(ctx, historyShown)
		total := len(entries)
		if err == nil && total == historyShown {
			total, err = store.Count(ctx)
		}
		w.do(func() {
			h.entries, h.total, h.err, h.listed, h.listing = entries, total, err, true, false
			if err != nil {
				w.log().Warn("the history could not be read", "err", err)
			}
			w.refreshHistoryRows()
			if h.again {
				h.again = false
				w.refreshHistory()
			}
		})
	}()
}

// refreshHistoryRows shows the entries read last.
func (w *SettingsWindow) refreshHistoryRows() {
	h := &w.history
	if h.box == nil {
		return
	}
	switch {
	case w.opts.History == nil:
		h.note.SetText(HistoryUnavailable)
	case h.err != nil:
		h.note.SetText("The history could not be read: " + h.err.Error())
	case !h.listed:
		h.note.SetText("Reading the history…")
	case len(h.entries) == 0:
		h.note.SetText(HistoryEmpty)
	case h.total > len(h.entries):
		h.note.SetText(fmt.Sprintf("The latest %d of %d dictations.", len(h.entries), h.total))
	default:
		h.note.SetText("")
	}
	h.note.Hidden = h.note.Text == ""
	if len(h.entries) > 0 {
		h.deleteAll.Show()
	} else {
		h.deleteAll.Hide()
	}
	rows := make([]fyne.CanvasObject, 0, len(h.entries))
	for _, e := range h.entries {
		rows = append(rows, w.historyRow(e))
	}
	h.box.Objects = rows
	h.box.Refresh()
	h.note.Refresh()
}

// historyRow shows one dictation: when, how long and with which model, its
// text, and what can be done with it.
func (w *SettingsWindow) historyRow(e history.Entry) fyne.CanvasObject {
	h := &w.history
	a := w.opts.HistoryActions
	text := paragraph(e.Text)
	if e.Text == "" {
		text.SetText(historyNothingDelivered)
		text.Importance = widget.LowImportance
	}
	text.Selectable = true

	buttons := container.NewHBox()
	if e.Text != "" {
		buttons.Add(widget.NewButtonWithIcon(LabelCopy, theme.ContentCopyIcon(), func() {
			w.app.Clipboard().SetContent(e.Text)
		}))
		if a.Paste != nil {
			buttons.Add(widget.NewButtonWithIcon(LabelPasteAgain, theme.ContentPasteIcon(), func() { a.Paste(e.Text) }))
		}
	}
	if e.Recording != "" && a.Play != nil {
		if h.playing == e.ID {
			buttons.Add(widget.NewButtonWithIcon(LabelStopPlaying, theme.MediaStopIcon(), w.stopPlaying))
		} else {
			buttons.Add(widget.NewButtonWithIcon(LabelPlay, theme.MediaPlayIcon(), func() { w.play(e) }))
		}
	}
	if e.Recording != "" && a.Retranscribe != nil {
		again := widget.NewButtonWithIcon(LabelRetranscribe, theme.ViewRefreshIcon(), func() { w.retranscribe(e) })
		if h.busy[e.ID] == HistoryRetranscribing {
			again.Disable()
		}
		buttons.Add(again)
	}
	buttons.Add(layout.NewSpacer())
	buttons.Add(widget.NewButtonWithIcon(LabelDeleteEntry, theme.DeleteIcon(), func() { w.deleteHistory(e.ID) }))

	heading := historyHeading(e, w.modelName(e.Model), historyNow())
	if w.cur.WebhookURL != "" {
		// The number the webhook got as transcription_id.
		heading += fmt.Sprintf(" · #%d", e.ID)
	}
	row := container.NewVBox(widget.NewSeparator(), note(heading), text)
	if status := h.busy[e.ID]; status != "" {
		row.Add(note(status))
	}
	row.Add(buttons)
	return row
}

// modelName is a model's display name, or its ID when the catalog has no
// such model.
func (w *SettingsWindow) modelName(id string) string {
	if m, ok := w.catalog().Model(id); ok {
		return m.Name
	}
	return id
}

// historyHeading says when a dictation was made, how long it lasted and
// which model transcribed it, such as "Today 14:22 · 6 s · Parakeet TDT
// 0.6B v2".
func historyHeading(e history.Entry, model string, now time.Time) string {
	at := e.At.In(now.Location())
	y1, m1, d1 := at.Date()
	y2, m2, d2 := now.Date()
	yesterday := now.AddDate(0, 0, -1)
	y3, m3, d3 := yesterday.Date()
	var when string
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		when = "Today " + at.Format("15:04")
	case y1 == y3 && m1 == m3 && d1 == d3:
		when = "Yesterday " + at.Format("15:04")
	case y1 == y2:
		when = at.Format("2 Jan 15:04")
	default:
		when = at.Format("2 Jan 2006 15:04")
	}
	parts := when
	if e.Source != "" {
		parts += " · " + e.Source
	}
	parts += " · " + formatLength(e.Duration)
	if model != "" {
		parts += " · " + model
	}
	return parts
}

// formatLength writes a recording's length in seconds, or minutes and
// seconds.
func formatLength(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	if s < 60 {
		return fmt.Sprintf("%d s", s)
	}
	return fmt.Sprintf("%d min %d s", s/60, s%60)
}

// play plays an entry's recording, stopping any other first.
func (w *SettingsWindow) play(e history.Entry) {
	w.stopPlaying()
	h := &w.history
	ctx, cancel := context.WithCancel(context.Background())
	h.playing, h.stopPlay = e.ID, cancel
	w.refreshHistoryRows()
	play := w.opts.HistoryActions.Play
	go func() {
		err := play(ctx, e.Recording)
		w.do(func() {
			if err != nil && ctx.Err() == nil {
				w.log().Warn("a recording could not be played", "path", e.Recording, "err", err)
				h.busy[e.ID] = "Could not play the recording: " + err.Error()
			}
			if h.playing == e.ID {
				h.playing, h.stopPlay = 0, nil
			}
			cancel()
			w.refreshHistoryRows()
		})
	}()
}

// stopPlaying stops the recording playing, if any.
func (w *SettingsWindow) stopPlaying() {
	h := &w.history
	if h.stopPlay != nil {
		h.stopPlay()
	}
	h.playing, h.stopPlay = 0, nil
	w.refreshHistoryRows()
}

// retranscribe transcribes an entry's recording again in the background
// and shows the new text.
func (w *SettingsWindow) retranscribe(e history.Entry) {
	h := &w.history
	h.busy[e.ID] = HistoryRetranscribing
	w.refreshHistoryRows()
	again := w.opts.HistoryActions.Retranscribe
	go func() {
		updated, err := again(context.Background(), e)
		w.do(func() {
			if err != nil {
				w.log().Warn("a recording could not be transcribed again", "entry", e.ID, "err", err)
				h.busy[e.ID] = "Could not transcribe it again: " + err.Error()
			} else {
				delete(h.busy, e.ID)
				for i := range h.entries {
					if h.entries[i].ID == e.ID {
						h.entries[i] = updated
					}
				}
			}
			w.refreshHistoryRows()
		})
	}()
}

// deleteHistory deletes one entry and its recording, in the background.
func (w *SettingsWindow) deleteHistory(id int64) {
	store := w.opts.History
	if store == nil {
		return
	}
	if w.history.playing == id {
		w.stopPlaying()
	}
	go func() {
		if err := store.Delete(context.Background(), id); err != nil {
			w.log().Warn("a dictation could not be deleted", "entry", id, "err", err)
		}
		w.do(w.refreshHistory)
	}()
}

// askDeleteAllHistory deletes every entry and recording once the user
// confirms.
func (w *SettingsWindow) askDeleteAllHistory() {
	store := w.opts.History
	if store == nil {
		return
	}
	w.confirm("Delete every dictation?", "Their text and recordings are removed from this computer.", LabelDeleteAll, func() {
		w.stopPlaying()
		go func() {
			if err := store.DeleteAll(context.Background()); err != nil {
				w.log().Warn("the history could not be deleted", "err", err)
			}
			w.do(w.refreshHistory)
		}()
	})
}
