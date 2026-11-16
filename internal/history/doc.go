// Package history keeps past dictations in SQLite, with their recordings as
// WAV files beside it, so the user can find, copy, paste again, replay or
// re-transcribe them.
//
// A Store holds one database and one recordings directory. It keeps as many
// of the latest dictations as its Retention allows, and their recordings for
// as long as it says, pruning after every dictation it adds and whenever the
// retention changes. The schema is versioned with SQLite's user_version and
// migrated forward when a Store opens. The package needs no CGo: the driver
// is modernc.org/sqlite.
package history
