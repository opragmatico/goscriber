package history

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
)

var ctx = context.Background()

// base is when the first test dictation starts; later ones follow a minute
// apart.
var base = time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC)

type testStore struct {
	*Store
	dbPath, dir string
}

func open(t *testing.T, r Retention) *testStore {
	t.Helper()
	root := t.TempDir()
	return reopen(t, filepath.Join(root, "history.sqlite3"), filepath.Join(root, "recordings"), r)
}

func reopen(t *testing.T, dbPath, dir string, r Retention) *testStore {
	t.Helper()
	s, err := Open(dbPath, dir, r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &testStore{Store: s, dbPath: dbPath, dir: dir}
}

// record is the n-th test dictation: a second of quiet tone.
func record(n int) Record {
	samples := make([]float32, 16000)
	for i := range samples {
		samples[i] = 0.25 * float32((i%32)-16) / 16
	}
	return Record{
		At:         base.Add(time.Duration(n) * time.Minute),
		Text:       "Dictation " + string(rune('A'+n)) + ".",
		RawText:    "dictation " + string(rune('a'+n)),
		Model:      "parakeet-tdt-0.6b-v2",
		Language:   "en",
		Duration:   time.Second,
		Samples:    samples,
		SampleRate: 16000,
	}
}

func (s *testStore) add(t *testing.T, n int) Entry {
	t.Helper()
	e, err := s.Add(ctx, record(n))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (s *testStore) texts(t *testing.T) []string {
	t.Helper()
	entries, err := s.List(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Text)
	}
	return out
}

// recordings lists the files in the recordings directory.
func (s *testStore) recordings(t *testing.T) []string {
	t.Helper()
	des, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, de := range des {
		out = append(out, de.Name())
	}
	return out
}

var keepAll = Retention{Limit: 100, Recordings: config.RetentionPreserveLimit}

func TestAddListGet(t *testing.T) {
	s := open(t, keepAll)
	first := s.add(t, 0)
	second := s.add(t, 1)

	entries, err := s.List(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != second.ID || entries[1].ID != first.ID {
		t.Fatalf("entries %+v, want the newest first", entries)
	}
	got, err := s.Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := record(0)
	if !got.At.Equal(want.At) || got.Text != want.Text || got.RawText != want.RawText || got.Model != want.Model ||
		got.Language != want.Language || got.Duration != want.Duration {
		t.Errorf("got %+v, want %+v", got, want)
	}
	samples, rate, err := audio.ReadWAVFile(got.Recording)
	if err != nil {
		t.Fatal(err)
	}
	if rate != 16000 || len(samples) != len(want.Samples) {
		t.Errorf("recording: %d samples at %d Hz", len(samples), rate)
	}
	if fi, err := os.Stat(got.Recording); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("recording mode %v, %v: recordings are private", fi.Mode(), err)
	}

	if one, err := s.List(ctx, 1); err != nil || len(one) != 1 || one[0].ID != second.ID {
		t.Errorf("List(1) = %+v, %v", one, err)
	}
	if n, err := s.Count(ctx); err != nil || n != 2 {
		t.Errorf("Count = %d, %v", n, err)
	}
	if _, err := s.Get(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get of a missing entry: %v", err)
	}
}

// Two dictations starting in the same millisecond keep both recordings.
func TestRecordingNamesDoNotCollide(t *testing.T) {
	s := open(t, keepAll)
	a, b := s.add(t, 0), s.add(t, 0)
	if a.Recording == b.Recording || len(s.recordings(t)) != 2 {
		t.Errorf("recordings %q and %q, files %v", a.Recording, b.Recording, s.recordings(t))
	}
}

func TestDeleteAndDeleteAll(t *testing.T) {
	s := open(t, keepAll)
	first := s.add(t, 0)
	s.add(t, 1)
	s.add(t, 2)

	if err := s.Delete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.texts(t); len(got) != 2 || got[1] != record(1).Text {
		t.Errorf("after Delete: %v", got)
	}
	if _, err := os.Stat(first.Recording); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the deleted entry's recording is still there: %v", err)
	}
	if err := s.Delete(ctx, first.ID); err != nil {
		t.Errorf("deleting it again: %v", err)
	}

	if err := s.DeleteAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.texts(t); len(got) != 0 {
		t.Errorf("after DeleteAll: %v", got)
	}
	if files := s.recordings(t); len(files) != 0 {
		t.Errorf("recordings left: %v", files)
	}
}

func TestRetranscribed(t *testing.T) {
	s := open(t, keepAll)
	e := s.add(t, 0)
	got, err := s.Retranscribed(ctx, e.ID, Transcript{Text: "Better.", RawText: "better", Model: "nemotron", Language: "en-US"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "Better." || got.RawText != "better" || got.Model != "nemotron" || got.Language != "en-US" ||
		!got.At.Equal(e.At) || got.Recording != e.Recording {
		t.Errorf("got %+v", got)
	}
	if _, err := s.Retranscribed(ctx, 999, Transcript{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing entry: %v", err)
	}
}

// history_limit keeps the latest dictations, with their recordings; 0 keeps
// none, so nothing is saved at all.
func TestRetentionByCount(t *testing.T) {
	s := open(t, Retention{Limit: 2, Recordings: config.RetentionPreserveLimit})
	oldest := s.add(t, 0)
	s.add(t, 1)
	s.add(t, 2)
	if got := s.texts(t); len(got) != 2 || got[0] != record(2).Text || got[1] != record(1).Text {
		t.Errorf("kept %v, want the two latest", got)
	}
	if _, err := os.Stat(oldest.Recording); !errors.Is(err, os.ErrNotExist) {
		t.Error("the pruned entry's recording is still there")
	}
	if files := s.recordings(t); len(files) != 2 {
		t.Errorf("recordings %v", files)
	}

	// Lowering the limit prunes at once.
	if err := s.SetRetention(ctx, Retention{Limit: 1, Recordings: config.RetentionPreserveLimit}); err != nil {
		t.Fatal(err)
	}
	if got := s.texts(t); len(got) != 1 || got[0] != record(2).Text {
		t.Errorf("after lowering the limit: %v", got)
	}

	if err := s.SetRetention(ctx, Retention{Limit: 0, Recordings: config.RetentionPreserveLimit}); err != nil {
		t.Fatal(err)
	}
	e, err := s.Add(ctx, record(3))
	if err != nil || e.ID != 0 {
		t.Errorf("Add with a limit of 0: %+v, %v", e, err)
	}
	if got, files := s.texts(t), s.recordings(t); len(got) != 0 || len(files) != 0 {
		t.Errorf("a limit of 0 kept %v and %v", got, files)
	}
}

// recording_retention keeps recordings for as long as it says, or none;
// the entries stay.
func TestRetentionOfRecordings(t *testing.T) {
	s := open(t, Retention{Limit: 10, Recordings: config.RetentionDays3})
	now := base.Add(4 * 24 * time.Hour)
	s.now = func() time.Time { return now }
	old := s.add(t, 0) // four days old: its recording goes at once
	recent, err := s.Add(ctx, Record{At: now.Add(-time.Hour), Text: "Recent.", Samples: make([]float32, 160), SampleRate: 16000})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, old.ID); got.Recording != "" {
		t.Errorf("a recording older than three days is kept: %q", got.Recording)
	}
	if _, err := os.Stat(old.Recording); !errors.Is(err, os.ErrNotExist) {
		t.Error("its file is still there")
	}
	if got, _ := s.Get(ctx, recent.ID); got.Recording == "" {
		t.Error("a recording from an hour ago is gone")
	}
	if len(s.texts(t)) != 2 {
		t.Error("dropping a recording must keep its entry")
	}

	// As time passes, the next prune drops it too.
	now = now.Add(3 * 24 * time.Hour)
	if err := s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, recent.ID); got.Recording != "" {
		t.Error("the recording outlived three days")
	}

	// "never" saves no recording and drops the ones kept.
	if err := s.SetRetention(ctx, Retention{Limit: 10, Recordings: config.RetentionWeeks2}); err != nil {
		t.Fatal(err)
	}
	week := s.add(t, 1) // a week old, within two weeks
	if week, _ = s.Get(ctx, week.ID); week.Recording == "" {
		t.Fatal("weeks_2 dropped a week-old recording")
	}
	if err := s.SetRetention(ctx, Retention{Limit: 10, Recordings: config.RetentionNever}); err != nil {
		t.Fatal(err)
	}
	none, err := s.Add(ctx, Record{At: now, Text: "None.", Samples: make([]float32, 160), SampleRate: 16000})
	if err != nil || none.Recording != "" {
		t.Errorf("never kept a recording: %+v, %v", none, err)
	}
	if files := s.recordings(t); len(files) != 0 {
		t.Errorf("never left recordings: %v", files)
	}
}

// Opening prunes what the retention does not keep, and the history survives
// a reopen.
func TestReopenPrunes(t *testing.T) {
	s := open(t, keepAll)
	for n := range 4 {
		s.add(t, n)
	}
	s.Close()
	again := reopen(t, s.dbPath, s.dir, Retention{Limit: 3, Recordings: config.RetentionPreserveLimit})
	if got := again.texts(t); len(got) != 3 || got[0] != record(3).Text {
		t.Errorf("after reopening with a limit of 3: %v", got)
	}
	if files := again.recordings(t); len(files) != 3 {
		t.Errorf("recordings %v", files)
	}
}

func TestMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.sqlite3")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	version := func() int {
		var v int
		if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	if err := migrate(ctx, db, migrations); err != nil {
		t.Fatal(err)
	}
	if v := version(); v != len(migrations) {
		t.Fatalf("version %d after migrating a new database", v)
	}
	if _, err := db.Exec(`INSERT INTO entries (at, text, raw_text, model, duration_ms) VALUES (1, 'kept', 'kept', 'm', 1000)`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, migrations); err != nil {
		t.Fatalf("migrating an up-to-date database: %v", err)
	}

	// A later migration runs once, on top of the data.
	next := append(append([]string(nil), migrations...), `ALTER TABLE entries ADD COLUMN starred INTEGER NOT NULL DEFAULT 0`)
	if err := migrate(ctx, db, next); err != nil {
		t.Fatal(err)
	}
	var text string
	var starred int
	if err := db.QueryRow(`SELECT text, starred FROM entries`).Scan(&text, &starred); err != nil || text != "kept" || starred != 0 {
		t.Errorf("after the new migration: %q, %d, %v", text, starred, err)
	}
	if v := version(); v != len(next) {
		t.Errorf("version %d", v)
	}

	// A failing migration leaves the schema where it was.
	broken := append(append([]string(nil), next...), `ALTER TABLE nowhere ADD COLUMN x`)
	if err := migrate(ctx, db, broken); err == nil {
		t.Error("a failing migration reported success")
	}
	if v := version(); v != len(next) {
		t.Errorf("version %d after a failed migration", v)
	}

	// A database from a newer Goscriber is refused, not changed.
	if err := migrate(ctx, db, migrations); !errors.Is(err, ErrNewer) {
		t.Errorf("an older schema list: %v", err)
	}
	db.Close()
	if _, err := Open(path, filepath.Join(filepath.Dir(path), "recordings"), keepAll); !errors.Is(err, ErrNewer) {
		t.Errorf("Open of a newer database: %v", err)
	}
}

// A history from before entries recorded their source file (schema 1)
// opens as it was, its dictations with no source, and takes entries
// transcribed from files.
func TestSourceColumnMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.sqlite3")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, migrations[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO entries (at, text, raw_text, model, duration_ms) VALUES (1, 'old dictation', 'old dictation', 'm', 1000)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s := reopen(t, path, filepath.Join(dir, "recordings"), keepAll)
	entries, err := s.List(ctx, 0)
	if err != nil || len(entries) != 1 || entries[0].Text != "old dictation" || entries[0].Source != "" {
		t.Fatalf("the old dictation after migrating: %+v, %v", entries, err)
	}
	rec := record(2)
	rec.Source = "interview.flac"
	added, err := s.Add(ctx, rec)
	if err != nil || added.Source != "interview.flac" {
		t.Fatalf("Add: %+v, %v", added, err)
	}
	got, err := s.Get(ctx, added.ID)
	if err != nil || got.Source != "interview.flac" {
		t.Errorf("Get: %+v, %v", got, err)
	}
}
