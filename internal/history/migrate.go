package history

import (
	"context"
	"database/sql"
	"fmt"
)

// migrations take the schema from one version to the next: migrations[i]
// goes from version i to i+1. Append to it; never edit a migration that has
// shipped.
var migrations = []string{
	// 1: the entries, with each recording's file name in the recordings
	// directory, or '' when it is not kept.
	`CREATE TABLE entries (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		at          INTEGER NOT NULL, -- when recording started, Unix milliseconds
		text        TEXT    NOT NULL, -- what was delivered
		raw_text    TEXT    NOT NULL, -- the model's transcript
		model       TEXT    NOT NULL,
		language    TEXT    NOT NULL DEFAULT '',
		duration_ms INTEGER NOT NULL,
		recording   TEXT    NOT NULL DEFAULT ''
	);
	CREATE INDEX entries_at ON entries (at);`,
	// 2: the name of the audio file an entry was transcribed from, or ''
	// for a dictation.
	`ALTER TABLE entries ADD COLUMN source TEXT NOT NULL DEFAULT '';`,
}

// migrate brings db's schema, versioned by SQLite's user_version, up to
// len(steps), each step in its own transaction. A schema newer than steps
// fails with ErrNewer and is left alone.
func migrate(ctx context.Context, db *sql.DB, steps []string) error {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read the schema version: %w", err)
	}
	if version > len(steps) {
		return fmt.Errorf("%w: schema version %d, this one knows %d", ErrNewer, version, len(steps))
	}
	for v := version; v < len(steps); v++ {
		if err := migrateStep(ctx, db, steps[v], v+1); err != nil {
			return fmt.Errorf("migrate the schema to version %d: %w", v+1, err)
		}
	}
	return nil
}

func migrateStep(ctx context.Context, db *sql.DB, step string, to int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, step); err != nil {
		return err
	}
	// PRAGMA takes no parameters; to is an int, so this is safe.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, to)); err != nil {
		return err
	}
	return tx.Commit()
}
