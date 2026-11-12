package models

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Errors the Manager returns; test for them with errors.Is.
var (
	// ErrUnknownModel: the catalog has no model with that id.
	ErrUnknownModel = errors.New("unknown model")
	// ErrUnknownQuant: the model has no file for that quantisation.
	ErrUnknownQuant = errors.New("unknown quantisation")
	// ErrNotDownloaded: the model's file is not on disk. Nothing downloads
	// it but Download.
	ErrNotDownloaded = errors.New("model not downloaded")
	// ErrCorrupt: the model's file is on disk but fails its checksum.
	// Download replaces it.
	ErrCorrupt = errors.New("model file is corrupt")
	// ErrBusy: the file is being downloaded right now.
	ErrBusy = errors.New("download in progress")
)

// State is where a model file stands.
type State int

// File states.
const (
	NotDownloaded State = iota
	Downloading
	Ready
	Corrupt
)

// String returns the state in words.
func (s State) String() string {
	switch s {
	case NotDownloaded:
		return "not downloaded"
	case Downloading:
		return "downloading"
	case Ready:
		return "ready"
	case Corrupt:
		return "corrupt"
	default:
		return "unknown"
	}
}

// Status is one model file's state.
type Status struct {
	Model *Model
	File  File
	State State
	// Path is the file to load when Ready, or the file that failed its
	// checksum when Corrupt.
	Path string
	// FromHFCache: Path is in the Hugging Face cache, not the models
	// directory. Delete leaves such files alone.
	FromHFCache bool
	// Done is how many bytes are on disk: so far while Downloading, or left
	// by an interrupted download that Download will resume.
	Done int64
}

// Percent is how much of the file is on disk, from 0 to 100.
func (s Status) Percent() int {
	if s.State == Ready {
		return 100
	}
	if s.File.Size <= 0 {
		return 0
	}
	return int(s.Done * 100 / s.File.Size)
}

// Progress reports a download in flight.
type Progress struct {
	Model string
	Quant string
	// File is the file being downloaded or verified now. A download may
	// fetch the VAD companion before the model.
	File string
	// Done and Total count bytes over every file of this download.
	Done, Total int64
	// Verifying: File is downloaded and its checksum is being checked.
	Verifying bool
}

// Options configure a Manager.
type Options struct {
	// Catalog lists the models; nil means Default().
	Catalog *Catalog
	// HFCache is the Hugging Face hub cache to look for existing files in,
	// usually HFCacheDir(os.Getenv); "" looks nowhere.
	HFCache string
	// Endpoint is the Hugging Face endpoint; "" means DefaultEndpoint.
	Endpoint string
	// Downloader fetches the files; nil means a default one.
	Downloader *Downloader
}

// Manager lists, downloads and deletes the catalog's model files in one
// directory. It is safe for concurrent use. Nothing it does touches the
// network except Download.
type Manager struct {
	catalog  *Catalog
	dir      string
	hfCache  string
	endpoint string
	dl       *Downloader
	verify   *verifier
	// freeSpace reports the bytes free on a directory's file system;
	// tests replace it.
	freeSpace func(dir string) (uint64, error)

	mu     sync.Mutex
	active map[string]*int64 // bytes on disk, by destination path
}

// NewManager returns a Manager for the model files in dir, which need not
// exist yet.
func NewManager(dir string, opts Options) *Manager {
	m := &Manager{
		catalog:   opts.Catalog,
		dir:       dir,
		hfCache:   opts.HFCache,
		endpoint:  opts.Endpoint,
		dl:        opts.Downloader,
		verify:    &verifier{path: filepath.Join(dir, verifiedFile)},
		freeSpace: freeSpace,
		active:    map[string]*int64{},
	}
	if m.catalog == nil {
		m.catalog = Default()
	}
	if m.endpoint == "" {
		m.endpoint = DefaultEndpoint
	}
	if m.dl == nil {
		m.dl = &Downloader{}
	}
	return m
}

// Catalog returns the catalog the Manager serves.
func (m *Manager) Catalog() *Catalog { return m.catalog }

// Dir returns the models directory.
func (m *Manager) Dir() string { return m.dir }

// List returns the status of every file in the catalog, model by model in
// catalog order and each model's quantisations in order. It reads only the
// disk; the first look at a file not seen before hashes it.
func (m *Manager) List() []Status {
	var out []Status
	for i := range m.catalog.Models {
		model := &m.catalog.Models[i]
		for _, f := range model.Files {
			out = append(out, m.status(context.Background(), model, f))
		}
	}
	return out
}

// Status returns the status of one model file; quant "" means the model's
// default quantisation.
func (m *Manager) Status(id, quant string) (Status, error) {
	model, f, err := m.lookup(id, quant)
	if err != nil {
		return Status{}, err
	}
	return m.status(context.Background(), model, f), nil
}

// Path returns the file to load for a model and quantisation ("" means the
// default one). It fails with ErrNotDownloaded, ErrCorrupt or ErrBusy when
// the file is not ready; it never downloads anything.
func (m *Manager) Path(id, quant string) (string, error) {
	st, err := m.Status(id, quant)
	if err != nil {
		return "", err
	}
	switch st.State {
	case Ready:
		return st.Path, nil
	case Downloading:
		return "", fmt.Errorf("%s %s: %w", id, st.File.Quant, ErrBusy)
	case Corrupt:
		return "", fmt.Errorf("%s %s: %w: %s", id, st.File.Quant, ErrCorrupt, st.Path)
	default:
		return "", fmt.Errorf("%s %s: %w", id, st.File.Quant, ErrNotDownloaded)
	}
}

// Any reports whether any ASR model is ready to load, in any quantisation,
// including from the Hugging Face cache. The app uses it to decide whether
// to offer the download step; it does not select a model.
func (m *Manager) Any() bool {
	for _, model := range m.catalog.ASR() {
		for _, f := range model.Files {
			if m.status(context.Background(), model, f).State == Ready {
				return true
			}
		}
	}
	return false
}

func (m *Manager) lookup(id, quant string) (*Model, File, error) {
	model, ok := m.catalog.Model(id)
	if !ok {
		ids := make([]string, len(m.catalog.Models))
		for i, mm := range m.catalog.Models {
			ids[i] = mm.ID
		}
		return nil, File{}, fmt.Errorf("%w %q (the catalog has %s)", ErrUnknownModel, id, strings.Join(ids, ", "))
	}
	f, ok := model.File(quant)
	if !ok {
		return nil, File{}, fmt.Errorf("%s: %w %q (it has %s)", id, ErrUnknownQuant, quant, strings.Join(model.Quants(), ", "))
	}
	return model, f, nil
}

func (m *Manager) local(f File) string { return filepath.Join(m.dir, f.Name) }

func (m *Manager) status(ctx context.Context, model *Model, f File) Status {
	st := Status{Model: model, File: f, State: NotDownloaded}
	dest := m.local(f)

	m.mu.Lock()
	done, busy := m.active[dest]
	if busy {
		st.State, st.Done = Downloading, *done
	}
	m.mu.Unlock()
	if busy {
		return st
	}

	switch ok, err := m.verify.check(ctx, dest, f.Size, f.SHA256); {
	case ok:
		st.State, st.Path, st.Done = Ready, dest, f.Size
		return st
	case err == nil, !errors.Is(err, fs.ErrNotExist):
		// On disk but wrong, or unreadable.
		st.State, st.Path = Corrupt, dest
		return st
	}
	st.Done = fileSize(dest + partSuffix)

	for _, p := range hfCandidates(m.hfCache, model.Repo, model.Revision, f.Name, f.SHA256) {
		if ok, _ := m.verify.check(ctx, p, f.Size, f.SHA256); ok {
			st.State, st.Path, st.FromHFCache, st.Done = Ready, p, true, f.Size
			return st
		}
	}
	return st
}

// Download fetches a model's file for quant ("" means the default one) into
// the models directory, together with the VAD companion when it is an ASR
// model and the VAD is not on disk yet. Files that are already ready,
// including from the Hugging Face cache, are skipped. It checks the free
// space before fetching anything and blocks until done.
//
// progress, when not nil, receives updates without blocking: a slow reader
// misses some, and the returned error is what counts. Cancelling ctx stops
// the download and keeps what it fetched for the next Download to resume.
func (m *Manager) Download(ctx context.Context, id, quant string, progress chan<- Progress) error {
	model, f, err := m.lookup(id, quant)
	if err != nil {
		return err
	}
	type job struct {
		model *Model
		file  File
	}
	var jobs []job
	if vad := m.catalog.VAD(); model.Kind == KindASR && vad != nil {
		vf, _ := vad.File("")
		jobs = append(jobs, job{vad, vf})
	}
	jobs = append(jobs, job{model, f})

	// Claim the files first, so a concurrent Download or Delete of the
	// same file fails instead of racing this one.
	var todo []job
	var claimed []string
	defer func() {
		m.mu.Lock()
		for _, dest := range claimed {
			delete(m.active, dest)
		}
		m.mu.Unlock()
	}()
	for _, j := range jobs {
		st := m.status(ctx, j.model, j.file)
		if st.State == Ready {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		dest := m.local(j.file)
		m.mu.Lock()
		if _, busy := m.active[dest]; busy {
			m.mu.Unlock()
			return fmt.Errorf("%s: %w", j.file.Name, ErrBusy)
		}
		m.active[dest] = new(int64)
		claimed = append(claimed, dest)
		m.mu.Unlock()
		if st.State == Corrupt {
			if err := os.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			m.verify.forget(dest)
		}
		todo = append(todo, j)
	}
	if len(todo) == 0 {
		return nil
	}

	var total, need int64
	for _, j := range todo {
		total += j.file.Size
		need += j.file.Size - min(fileSize(m.local(j.file)+partSuffix), j.file.Size)
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	if free, err := m.freeSpace(m.dir); err == nil && uint64(need)+spaceHeadroom > free {
		return fmt.Errorf("%w in %s: need %s, have %s", ErrNoSpace, m.dir, FormatBytes(need+spaceHeadroom), FormatBytes(int64(free)))
	}

	var before int64 // bytes of the files already fetched
	var last time.Time
	send := func(p Progress, force bool) {
		if progress == nil || (!force && time.Since(last) < 100*time.Millisecond) {
			return
		}
		last = time.Now()
		select {
		case progress <- p:
		default:
		}
	}
	for _, j := range todo {
		dest := m.local(j.file)
		p := Progress{Model: model.ID, Quant: f.Quant, File: j.file.Name, Total: total}
		err := m.dl.Fetch(ctx, Target{
			URL:    j.model.URL(m.endpoint, j.file),
			Dest:   dest,
			Size:   j.file.Size,
			SHA256: j.file.SHA256,
		}, func(done int64) {
			m.mu.Lock()
			*m.active[dest] = done
			m.mu.Unlock()
			p.Done = before + done
			send(p, done == j.file.Size)
		}, func() {
			p.Verifying = true
			send(p, true)
		})
		if err != nil {
			return fmt.Errorf("models: download %s: %w", j.model.ID, err)
		}
		m.verify.record(dest, j.file.SHA256)
		before += j.file.Size
	}
	return nil
}

// spaceHeadroom is free space Download leaves on the disk beyond the files.
const spaceHeadroom = 32 << 20

// Delete removes every file of a model from the models directory, partial
// downloads included. It leaves copies in the Hugging Face cache alone, as
// other tools may use them, and fails with ErrNotDownloaded when there was
// nothing to remove and ErrBusy while one of the files is downloading.
func (m *Manager) Delete(id string) error {
	model, ok := m.catalog.Model(id)
	if !ok {
		_, _, err := m.lookup(id, "")
		return err
	}
	m.mu.Lock()
	for _, f := range model.Files {
		if _, busy := m.active[m.local(f)]; busy {
			m.mu.Unlock()
			return fmt.Errorf("%s: %w", f.Name, ErrBusy)
		}
	}
	m.mu.Unlock()

	removed := 0
	var errs []error
	for _, f := range model.Files {
		for _, p := range []string{m.local(f), m.local(f) + partSuffix} {
			switch err := os.Remove(p); {
			case err == nil:
				removed++
			case !errors.Is(err, fs.ErrNotExist):
				errs = append(errs, err)
			}
		}
		m.verify.forget(m.local(f))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if removed == 0 {
		return fmt.Errorf("%s: %w", id, ErrNotDownloaded)
	}
	return nil
}

// FormatBytes formats n with a decimal unit, as "742 MB" or "1.3 GB".
func FormatBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v, exp := float64(n), 0
	for v >= unit && exp < 4 {
		v /= unit
		exp++
	}
	suffix := []string{"", "kB", "MB", "GB", "TB"}[exp]
	if v < 10 {
		return fmt.Sprintf("%.1f %s", v, suffix)
	}
	return fmt.Sprintf("%.0f %s", v, suffix)
}
