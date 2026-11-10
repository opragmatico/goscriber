package logging

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
)

// rotatingFile is an io.Writer that appends to path and, when the next write
// would push it past maxSize, shifts path → path.1 → path.2 … keeping at most
// maxFiles files in total. slog handlers write each record in one Write call,
// so a record is never split across files.
type rotatingFile struct {
	path     string
	maxSize  int64
	maxFiles int

	mu   sync.Mutex
	f    *os.File
	size int64
}

func openRotating(path string, maxSize int64, maxFiles int) (*rotatingFile, error) {
	w := &rotatingFile{path: path, maxSize: maxSize, maxFiles: maxFiles}
	if err := w.open(os.O_APPEND); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingFile) open(mode int) error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|mode, 0o600)
	if err != nil {
		return fmt.Errorf("logging: open log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("logging: stat log file: %w", err)
	}
	w.f, w.size = f, info.Size()
	return nil
}

func (w *rotatingFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, fs.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingFile) rotate() error {
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("logging: rotate: %w", err)
	}
	w.f = nil
	for i := w.maxFiles - 1; i >= 1; i-- {
		src := w.path
		if i > 1 {
			src = fmt.Sprintf("%s.%d", w.path, i-1)
		}
		if err := os.Rename(src, fmt.Sprintf("%s.%d", w.path, i)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			// Keep logging into the same file rather than losing records.
			return w.open(os.O_APPEND)
		}
	}
	return w.open(os.O_TRUNC)
}

func (w *rotatingFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
