package models

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// verifiedFile is the name of the file in the models directory that
// remembers which files have been hashed, so a model is not re-hashed (about
// a second for 700 MB) every time Goscriber starts.
const verifiedFile = "verified.json"

// verifier checks files against the catalog's SHA-256, hashing each file
// only once for as long as its size and modification time stay the same.
type verifier struct {
	path string // where the records persist; "" keeps them in memory only

	mu      sync.Mutex
	loaded  bool
	records map[string]record // by file path
}

type record struct {
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
	SHA256  string    `json:"sha256"`
}

// check reports whether the file at path is size bytes long with the given
// SHA-256. A file of the wrong size fails without being read.
func (v *verifier) check(ctx context.Context, path string, size int64, sha string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if fi.Size() != size {
		return false, nil
	}
	v.mu.Lock()
	v.load()
	r, ok := v.records[path]
	v.mu.Unlock()
	if ok && r.Size == fi.Size() && r.ModTime.Equal(fi.ModTime()) {
		return r.SHA256 == sha, nil
	}
	got, err := hashFile(ctx, path)
	if err != nil {
		return false, err
	}
	v.put(path, fi, got)
	return got == sha, nil
}

// record notes that the file at path, which was just verified, has the
// given SHA-256.
func (v *verifier) record(path, sha string) {
	if fi, err := os.Stat(path); err == nil {
		v.put(path, fi, sha)
	}
}

// forget drops what is known about path.
func (v *verifier) forget(path string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.load()
	if _, ok := v.records[path]; ok {
		delete(v.records, path)
		v.save()
	}
}

func (v *verifier) put(path string, fi fs.FileInfo, sha string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.load()
	v.records[path] = record{Size: fi.Size(), ModTime: fi.ModTime(), SHA256: sha}
	v.save()
}

// load reads the records once. A missing or unreadable file means none.
func (v *verifier) load() {
	if v.loaded {
		return
	}
	v.loaded = true
	v.records = map[string]record{}
	if v.path == "" {
		return
	}
	if data, err := os.ReadFile(v.path); err == nil {
		_ = json.Unmarshal(data, &v.records)
	}
}

// save writes the records of files that still exist, atomically. It is best
// effort: without it, files are only hashed again.
func (v *verifier) save() {
	if v.path == "" {
		return
	}
	for p := range v.records {
		if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
			delete(v.records, p)
		}
	}
	data, err := json.MarshalIndent(v.records, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(v.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, verifiedFile+".*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), v.path) != nil {
		_ = os.Remove(tmp.Name())
	}
}
