package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// SchemaVersion is the settings layout this build reads and writes. Bump it
// only together with a new entry in migrations.
const SchemaVersion = 1

// A migration upgrades a raw settings document by one schema version, in place.
type migration func(doc map[string]json.RawMessage) error

// migrations[i] upgrades a document from version i to i+1, so
// len(migrations) == SchemaVersion.
var migrations = []migration{
	// v0 is any file written without a schema_version. Its layout is the v1
	// layout, so there is nothing to move.
	func(map[string]json.RawMessage) error { return nil },
}

// LoadReport tells the caller what Load had to do, so the app can tell the user.
type LoadReport struct {
	// Created is true when there was no settings file and defaults are in use.
	Created bool
	// FromVersion is the schema version found in the file (0 when absent).
	FromVersion int
	// Migrated is true when the file was upgraded from an older schema.
	Migrated bool
	// Newer is true when the file was written by a newer Goscriber. Known
	// fields are used; the original is kept at BackupPath.
	Newer bool
	// Reset is true when the file could not be parsed and defaults are in use.
	// The original is kept at BackupPath.
	Reset bool
	// BackupPath is where the original file was copied or moved, if anywhere.
	BackupPath string
	// Fixed lists the JSON names of fields whose stored values were invalid and
	// were replaced with defaults.
	Fixed []string
}

// now is replaced in tests.
var now = time.Now

// Load reads the settings file at path. A missing file yields defaults; an
// unparsable file is moved aside and yields defaults; an older file is
// migrated; unknown fields are ignored and missing fields take their default.
// Only I/O errors other than "not found" are returned.
func Load(path string) (Settings, LoadReport, error) {
	return load(path, migrations)
}

func load(path string, migs []migration) (Settings, LoadReport, error) {
	target := len(migs)
	def := Defaults()
	def.SchemaVersion = target
	var rep LoadReport

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		rep.Created = true
		return def, rep, nil
	}
	if err != nil {
		return Settings{}, rep, fmt.Errorf("config: read settings: %w", err)
	}

	var doc map[string]json.RawMessage
	version, perr := parse(data, &doc)
	if perr != nil {
		backup := fmt.Sprintf("%s.corrupt-%s", path, now().UTC().Format("20060102T150405Z"))
		if err := os.Rename(path, backup); err != nil {
			return Settings{}, rep, fmt.Errorf("config: move aside unreadable settings: %w", err)
		}
		rep.Reset, rep.BackupPath = true, backup
		return def, rep, nil
	}
	rep.FromVersion = version

	switch {
	case version > target:
		backup := fmt.Sprintf("%s.v%d.bak", path, version)
		if err := os.WriteFile(backup, data, 0o600); err != nil {
			return Settings{}, rep, fmt.Errorf("config: back up newer settings: %w", err)
		}
		rep.Newer, rep.BackupPath = true, backup
	case version < target:
		for v := version; v < target; v++ {
			if err := migs[v](doc); err != nil {
				return Settings{}, rep, fmt.Errorf("config: migrate settings v%d to v%d: %w", v, v+1, err)
			}
		}
		rep.Migrated = true
	}

	s, badTypes := decodeOnto(def, doc)
	rep.Fixed = append(badTypes, s.normalize(def)...)
	s.SchemaVersion = target
	return s, rep, nil
}

// parse decodes the document and its schema_version (0 when absent).
func parse(data []byte, doc *map[string]json.RawMessage) (int, error) {
	if err := json.Unmarshal(data, doc); err != nil {
		return 0, err
	}
	if *doc == nil {
		return 0, errors.New("settings file is not a JSON object")
	}
	raw, ok := (*doc)["schema_version"]
	if !ok {
		return 0, nil
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil || v < 0 {
		return 0, fmt.Errorf("invalid schema_version %s", raw)
	}
	return v, nil
}

// decodeOnto applies each field of doc to a copy of base, one at a time, so a
// field with the wrong JSON type falls back to its default without discarding
// the rest. It returns the names of the fields it had to skip.
func decodeOnto(base Settings, doc map[string]json.RawMessage) (Settings, []string) {
	s := base.Clone()
	var bad []string
	for key, raw := range doc {
		if key == "schema_version" {
			continue
		}
		one, err := json.Marshal(map[string]json.RawMessage{key: raw})
		if err != nil {
			bad = append(bad, key)
			continue
		}
		next := s.Clone()
		if err := json.Unmarshal(one, &next); err != nil {
			bad = append(bad, key)
			continue
		}
		s = next
	}
	return s, bad
}

// Save writes s to path atomically: a temporary file in the same directory is
// written, synced and renamed over the old file, so a crash never leaves a
// half-written settings file.
func Save(path string, s Settings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode settings: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := EnsureDir(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("config: save settings: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("config: save settings: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("config: save settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: save settings: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("config: save settings: %w", err)
	}
	return nil
}
