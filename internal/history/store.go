package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite" // the "sqlite" database/sql driver, in pure Go

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
)

// Errors a Store returns; test for them with errors.Is.
var (
	// ErrNotFound: no entry has that ID.
	ErrNotFound = errors.New("history: no such entry")
	// ErrNewer: the database was written by a newer Goscriber, whose schema
	// this one does not know.
	ErrNewer = errors.New("history: database is from a newer Goscriber")
)

// Entry is one dictation in the history.
type Entry struct {
	// ID identifies the entry, and the transcription to the webhook. IDs
	// only go up: one is never reused, even after its entry is deleted.
	ID int64
	// At is when recording started.
	At time.Time
	// Text is what was delivered, after the text stages, or the latest
	// re-transcription or re-processing.
	Text string
	// RawText is the model's transcript, before the text stages: what
	// re-processing starts from.
	RawText string
	// ProcessedText is what LLM post-processing made of the transcript, or
	// "" when it was not post-processed.
	ProcessedText string
	// Model is the catalog ID of the model that transcribed it.
	Model string
	// Language is the transcript's language when known, such as "en-US".
	Language string
	// Duration is how long the recording lasted.
	Duration time.Duration
	// Recording is the WAV file's path, or "" when its recording is not
	// kept.
	Recording string
	// Source is the name of the audio file it was transcribed from, or ""
	// for a dictation.
	Source string
	// Visible reports whether the History page shows the entry. Only Add
	// returns a hidden one: added while the history keeps no dictations, it
	// is pruned at once.
	Visible bool
}

// Record is a finished dictation to add.
type Record struct {
	At      time.Time
	Text    string
	RawText string
	// ProcessedText is what LLM post-processing made of RawText, or "".
	ProcessedText string
	Model         string
	Language      string
	Duration      time.Duration
	// Samples are the recording, mono at SampleRate Hz. None keeps no
	// recording.
	Samples    []float32
	SampleRate int
	// Source is the name of the audio file it was transcribed from, or ""
	// for a dictation.
	Source string
}

// Retention says what a Store keeps, from the history_limit and
// recording_retention settings.
type Retention struct {
	// Limit is how many of the latest dictations are kept; 0 keeps none.
	Limit int
	// Recordings is how long their recordings are kept.
	Recordings config.RecordingRetention
}

// RetentionFrom is the retention the settings ask for.
func RetentionFrom(s config.Settings) Retention {
	return Retention{Limit: s.HistoryLimit, Recordings: s.RecordingRetention}
}

// maxAge is how long a recording is kept under r, or 0 for as long as its
// entry; keep reports false when no recording is kept at all.
func (r Retention) maxAge() (age time.Duration, keep bool) {
	const day = 24 * time.Hour
	switch r.Recordings {
	case config.RetentionNever:
		return 0, false
	case config.RetentionDays3:
		return 3 * day, true
	case config.RetentionWeeks2:
		return 14 * day, true
	case config.RetentionMonths3:
		return 90 * day, true
	default: // preserve_limit
		return 0, true
	}
}

// Store is the history: a SQLite database and a directory of recordings.
// It is safe for concurrent use. Close it when done.
type Store struct {
	db  *sql.DB
	dir string
	// now is the clock recordings age by; tests replace it.
	now func() time.Time

	mu        sync.Mutex // serialises changes, so pruning and adding see each other
	retention Retention
}

// Open opens the history database at dbPath, creating it and its directory
// if needed and migrating an older schema, with recordings in
// recordingsDir. It prunes what r does not keep.
func Open(dbPath, recordingsDir string, r Retention) (*Store, error) {
	if err := config.EnsureDir(filepath.Dir(dbPath)); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("history: open %s: %w", dbPath, err)
	}
	// One connection: every statement runs in turn, so the store never sees
	// SQLITE_BUSY from itself.
	db.SetMaxOpenConns(1)
	if err := migrate(context.Background(), db, migrations); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("history: %s: %w", dbPath, err)
	}
	s := &Store{db: db, dir: recordingsDir, now: time.Now, retention: r}
	if err := s.Prune(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Retention is what the store keeps now.
func (s *Store) Retention() Retention {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retention
}

// SetRetention changes what the store keeps, and prunes at once what it no
// longer does.
func (s *Store) SetRetention(ctx context.Context, r Retention) error {
	s.mu.Lock()
	changed := r != s.retention
	s.retention = r
	s.mu.Unlock()
	if !changed {
		return nil
	}
	return s.Prune(ctx)
}

// Add saves a dictation, with its recording unless the retention keeps
// none, then prunes. With a Limit of 0 the dictation is added hidden,
// without its recording, and pruned at once: the Entry Add returns is not
// Visible, and is there for its ID, which the webhook sends. The app calls
// Add then only when a webhook is set.
func (s *Store) Add(ctx context.Context, rec Record) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := Entry{
		At: rec.At, Text: rec.Text, RawText: rec.RawText, ProcessedText: rec.ProcessedText,
		Model: rec.Model, Language: rec.Language, Duration: rec.Duration, Source: rec.Source,
		Visible: s.retention.Limit > 0,
	}
	file := ""
	if _, keep := s.retention.maxAge(); e.Visible && keep && len(rec.Samples) > 0 && rec.SampleRate > 0 {
		var err error
		if file, err = s.writeRecording(rec); err != nil {
			return Entry{}, err
		}
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO entries (at, text, raw_text, processed_text, model, language, duration_ms, recording, source, visible) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.At.UnixMilli(), e.Text, e.RawText, e.ProcessedText, e.Model, e.Language, e.Duration.Milliseconds(), file, e.Source, e.Visible)
	if err != nil {
		s.removeRecording(file)
		return Entry{}, fmt.Errorf("history: add: %w", err)
	}
	if e.ID, err = res.LastInsertId(); err != nil {
		return Entry{}, fmt.Errorf("history: add: %w", err)
	}
	e.Recording = s.recordingPath(file)
	if err := s.pruneLocked(ctx); err != nil {
		return e, err
	}
	return e, nil
}

// writeRecording writes rec's samples to a new WAV file in the recordings
// directory, named after when recording started, and returns its name.
func (s *Store) writeRecording(rec Record) (string, error) {
	if err := config.EnsureDir(s.dir); err != nil {
		return "", err
	}
	base := rec.At.UTC().Format("20060102-150405.000")
	for i := 1; ; i++ {
		name := base + ".wav"
		if i > 1 {
			name = fmt.Sprintf("%s-%d.wav", base, i)
		}
		f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("history: save the recording: %w", err)
		}
		if err := audio.WriteWAV(f, rec.Samples, rec.SampleRate); err != nil {
			_ = f.Close()
			s.removeRecording(name)
			return "", fmt.Errorf("history: save the recording: %w", err)
		}
		if err := f.Close(); err != nil {
			s.removeRecording(name)
			return "", fmt.Errorf("history: save the recording: %w", err)
		}
		return name, nil
	}
}

// List returns up to limit visible entries, the newest first; limit 0 or
// less returns them all.
func (s *Store) List(ctx context.Context, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM entries WHERE visible ORDER BY at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("history: list: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, err := s.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("history: list: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("history: list: %w", err)
	}
	return out, nil
}

// Count returns how many visible entries there are.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM entries WHERE visible`).Scan(&n); err != nil {
		return 0, fmt.Errorf("history: count: %w", err)
	}
	return n, nil
}

// Get returns the visible entry with id, or an error matching ErrNotFound.
func (s *Store) Get(ctx context.Context, id int64) (Entry, error) {
	e, err := s.scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM entries WHERE id = ? AND visible`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, fmt.Errorf("%w: %d", ErrNotFound, id)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("history: get %d: %w", id, err)
	}
	return e, nil
}

// Transcript is a new transcript of an entry's recording.
type Transcript struct {
	Text, RawText, ProcessedText, Model, Language string
}

// Retranscribed replaces the transcript of entry id with t, keeping its
// time and recording, and returns the entry as it is now.
func (s *Store) Retranscribed(ctx context.Context, id int64, t Transcript) (Entry, error) {
	return s.update(ctx, id, `text = ?, raw_text = ?, processed_text = ?, model = ?, language = ?`,
		t.Text, t.RawText, t.ProcessedText, t.Model, t.Language)
}

// Processing is a new processing of an entry's transcript, its RawText,
// by the text stages.
type Processing struct {
	// Text is the transcript after the text stages, and ProcessedText what
	// LLM post-processing made of it, or "".
	Text, ProcessedText string
}

// Reprocessed replaces the text of entry id with p, keeping its transcript,
// and returns the entry as it is now.
func (s *Store) Reprocessed(ctx context.Context, id int64, p Processing) (Entry, error) {
	return s.update(ctx, id, `text = ?, processed_text = ?`, p.Text, p.ProcessedText)
}

// update sets columns of the visible entry id and returns it as it is now.
func (s *Store) update(ctx context.Context, id int64, set string, args ...any) (Entry, error) {
	s.mu.Lock()
	res, err := s.db.ExecContext(ctx, `UPDATE entries SET `+set+` WHERE id = ? AND visible`, append(args, id)...)
	s.mu.Unlock()
	if err != nil {
		return Entry{}, fmt.Errorf("history: update %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return Entry{}, fmt.Errorf("%w: %d", ErrNotFound, id)
	}
	return s.Get(ctx, id)
}

// Delete removes entry id and its recording. A missing entry is not an
// error.
func (s *Store) Delete(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteWhere(ctx, `id = ?`, id)
}

// DeleteAll removes every entry and recording.
func (s *Store) DeleteAll(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteWhere(ctx, `1`)
}

// Prune removes the hidden entries and the visible ones beyond the
// retention's limit, with their recordings, and the recordings older than
// it keeps.
func (s *Store) Prune(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneLocked(ctx)
}

func (s *Store) pruneLocked(ctx context.Context) error {
	r := s.retention
	err := s.deleteWhere(ctx,
		`NOT visible OR id IN (SELECT id FROM entries WHERE visible ORDER BY at DESC, id DESC LIMIT -1 OFFSET ?)`, max(r.Limit, 0))
	if err != nil {
		return err
	}
	switch age, keep := r.maxAge(); {
	case !keep:
		return s.dropRecordingsWhere(ctx, `recording != ''`)
	case age > 0:
		return s.dropRecordingsWhere(ctx, `recording != '' AND at < ?`, s.now().Add(-age).UnixMilli())
	}
	return nil
}

// deleteWhere deletes the entries matching where, and their recordings.
// s.mu must be held.
func (s *Store) deleteWhere(ctx context.Context, where string, args ...any) error {
	files, err := s.recordingsWhere(ctx, where, args...)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM entries WHERE `+where, args...); err != nil {
		return fmt.Errorf("history: delete: %w", err)
	}
	for _, f := range files {
		s.removeRecording(f)
	}
	return nil
}

// dropRecordingsWhere deletes the recordings of the entries matching where
// and keeps the entries. s.mu must be held.
func (s *Store) dropRecordingsWhere(ctx context.Context, where string, args ...any) error {
	files, err := s.recordingsWhere(ctx, where, args...)
	if err != nil || len(files) == 0 {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE entries SET recording = '' WHERE `+where, args...); err != nil {
		return fmt.Errorf("history: drop recordings: %w", err)
	}
	for _, f := range files {
		s.removeRecording(f)
	}
	return nil
}

func (s *Store) recordingsWhere(ctx context.Context, where string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT recording FROM entries WHERE recording != '' AND (`+where+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("history: find recordings: %w", err)
	}
	defer rows.Close()
	var files []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, fmt.Errorf("history: find recordings: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// removeRecording deletes a recording by name; one already gone is fine.
func (s *Store) removeRecording(name string) {
	if name != "" {
		_ = os.Remove(filepath.Join(s.dir, filepath.Base(name)))
	}
}

// recordingPath is the path of a recording stored by name, or "".
func (s *Store) recordingPath(name string) string {
	if name == "" {
		return ""
	}
	return filepath.Join(s.dir, filepath.Base(name))
}

const columns = `id, at, text, raw_text, processed_text, model, language, duration_ms, recording, source, visible`

func (s *Store) scan(row interface{ Scan(...any) error }) (Entry, error) {
	var (
		e        Entry
		at, dur  int64
		filename string
	)
	if err := row.Scan(&e.ID, &at, &e.Text, &e.RawText, &e.ProcessedText, &e.Model, &e.Language, &dur, &filename, &e.Source, &e.Visible); err != nil {
		return Entry{}, err
	}
	e.At = time.UnixMilli(at)
	e.Duration = time.Duration(dur) * time.Millisecond
	e.Recording = s.recordingPath(filename)
	return e, nil
}
