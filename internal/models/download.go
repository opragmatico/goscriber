package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultEndpoint is the Hugging Face hub. The HF_ENDPOINT environment
// variable overrides it, as it does for Hugging Face's own tools.
const DefaultEndpoint = "https://huggingface.co"

// partSuffix marks a download in progress. Download resumes from it.
const partSuffix = ".part"

// Errors a download can fail with, besides the context's error when it is
// cancelled; test for them with errors.Is.
var (
	// ErrChecksum: the downloaded file is not the one the catalog pins (its
	// size or SHA-256 differs). The partial file is deleted.
	ErrChecksum = errors.New("checksum mismatch")
	// ErrNoSpace: the disk does not have room for the download.
	ErrNoSpace = errors.New("not enough free disk space")
)

// Downloader fetches single files over HTTP. The zero value is ready to use.
type Downloader struct {
	// Client sends the requests. Nil means a client with connection and
	// response-header timeouts but no overall one, as downloads are large.
	Client *http.Client
	// UserAgent, when set, is sent with each request.
	UserAgent string
	// Attempts is how many times one file is tried before giving up on
	// transient failures (network errors, stalls, HTTP 408, 429 and 5xx).
	// Each retry resumes where the last one stopped. Zero means 5.
	Attempts int
	// Backoff is the wait before retry n (1 for the first retry). Nil means
	// 1 s, doubling up to 30 s.
	Backoff func(n int) time.Duration
	// StallTimeout ends an attempt that receives no data for this long, so
	// a wedged connection is retried instead of hanging. Zero means 60 s.
	StallTimeout time.Duration
}

// Target is one file to download.
type Target struct {
	URL string
	// Dest is where the verified file ends up. The download goes to
	// Dest+".part" until then.
	Dest   string
	Size   int64
	SHA256 string
}

// Fetch downloads t.URL to t.Dest, resuming from t.Dest+".part" when a
// previous attempt left one. It verifies the size and SHA-256 and only then
// renames the file into place, so Dest is either absent or complete.
// progress, when not nil, is called with the bytes on disk so far, and
// verifying with no arguments before the checksum pass.
//
// Cancelling ctx stops the download and keeps the partial file for the next
// Fetch to resume; the error then wraps ctx.Err(). A checksum mismatch
// deletes the partial file and fails with ErrChecksum, after starting over
// once if the download had resumed from an earlier partial file, which may
// have been the corrupt part.
func (d *Downloader) Fetch(ctx context.Context, t Target, progress func(done int64), verifying func()) error {
	if progress == nil {
		progress = func(int64) {}
	}
	if err := os.MkdirAll(filepath.Dir(t.Dest), 0o700); err != nil {
		return err
	}
	part := t.Dest + partSuffix
	resumed := fileSize(part) > 0
	for {
		if err := d.download(ctx, t, part, progress); err != nil {
			return err
		}
		if verifying != nil {
			verifying()
		}
		err := verifyFile(ctx, part, t.Size, t.SHA256)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return fmt.Errorf("%s: %w", filepath.Base(t.Dest), ctx.Err())
		}
		_ = os.Remove(part)
		if errors.Is(err, ErrChecksum) && resumed {
			resumed = false
			progress(0)
			continue
		}
		return fmt.Errorf("%s: %w", filepath.Base(t.Dest), err)
	}
	if err := os.Rename(part, t.Dest); err != nil {
		return err
	}
	syncDir(filepath.Dir(t.Dest))
	return nil
}

// download fills part with t's bytes, retrying transient failures.
func (d *Downloader) download(ctx context.Context, t Target, part string, progress func(int64)) error {
	attempts := d.Attempts
	if attempts <= 0 {
		attempts = 5
	}
	for n := 1; ; n++ {
		err := d.attempt(ctx, t, part, progress)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("%s: %w", filepath.Base(t.Dest), ctx.Err())
		}
		var te *transientError
		if !errors.As(err, &te) || n >= attempts {
			return fmt.Errorf("%s: %w", filepath.Base(t.Dest), err)
		}
		timer := time.NewTimer(d.backoff(n))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%s: %w", filepath.Base(t.Dest), ctx.Err())
		}
	}
}

func (d *Downloader) backoff(n int) time.Duration {
	if d.Backoff != nil {
		return d.Backoff(n)
	}
	return min(time.Second<<(n-1), 30*time.Second)
}

func (d *Downloader) stallTimeout() time.Duration {
	if d.StallTimeout > 0 {
		return d.StallTimeout
	}
	return 60 * time.Second
}

var defaultClient = func() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: tr}
}()

func (d *Downloader) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return defaultClient
}

// errStalled ends an attempt that stopped receiving data.
var errStalled = errors.New("download stalled")

// transientError marks a failure worth retrying.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// StatusError is an HTTP response Fetch could not use.
type StatusError struct {
	URL  string
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("GET %s: %d %s", e.URL, e.Code, http.StatusText(e.Code))
}

// attempt makes one request and appends what it receives to part. It
// returns nil once part holds t.Size bytes.
func (d *Downloader) attempt(ctx context.Context, t Target, part string, progress func(int64)) error {
	off := fileSize(part)
	if off > t.Size {
		// Larger than the file can be: start over.
		if err := os.Remove(part); err != nil {
			return err
		}
		off = 0
	}
	progress(off)
	if off == t.Size {
		return nil // complete; the checksum decides whether it is good
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stall := time.AfterFunc(d.stallTimeout(), func() { cancel(errStalled) })
	defer stall.Stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		return err
	}
	if d.UserAgent != "" {
		req.Header.Set("User-Agent", d.UserAgent)
	}
	if off > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(off, 10)+"-")
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return netError(ctx, err)
	}
	defer resp.Body.Close()

	flags := os.O_WRONLY | os.O_CREATE
	switch {
	case resp.StatusCode == http.StatusPartialContent && off > 0:
		if start, ok := contentRangeStart(resp.Header.Get("Content-Range")); !ok || start != off {
			// Not the bytes asked for: drop the partial file and start over.
			if err := os.Remove(part); err != nil {
				return err
			}
			return &transientError{fmt.Errorf("server resumed at %q, not byte %d", resp.Header.Get("Content-Range"), off)}
		}
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		// A fresh download, or a server that ignored the Range header.
		off = 0
		progress(0)
		flags |= os.O_TRUNC
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && off > 0:
		// The file is smaller than the partial one, so the partial one is
		// not part of it.
		if err := os.Remove(part); err != nil {
			return err
		}
		return &transientError{&StatusError{URL: t.URL, Code: resp.StatusCode}}
	default:
		err := &StatusError{URL: t.URL, Code: resp.StatusCode}
		if c := resp.StatusCode; c == http.StatusRequestTimeout || c == http.StatusTooManyRequests || c >= 500 {
			return &transientError{err}
		}
		return err
	}

	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return err
	}
	buf := make([]byte, 256<<10)
	for off < t.Size {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			stall.Reset(d.stallTimeout())
			if int64(n) > t.Size-off {
				_ = f.Close()
				_ = os.Remove(part)
				return fmt.Errorf("%w: server sent more than %d bytes", ErrChecksum, t.Size)
			}
			if _, err := f.Write(buf[:n]); err != nil {
				_ = f.Close()
				return err
			}
			off += int64(n)
			progress(off)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = f.Close()
			return netError(ctx, rerr)
		}
	}
	if off == t.Size {
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if off < t.Size {
		return &transientError{fmt.Errorf("connection closed after %d of %d bytes", off, t.Size)}
	}
	return nil
}

// netError classifies a failure to send a request or read its body: a stall
// or a network error is transient; the caller's cancellation is not.
func netError(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); errors.Is(cause, errStalled) {
		return &transientError{errStalled}
	}
	return &transientError{err}
}

// contentRangeStart parses the first byte of "bytes <start>-<end>/<total>".
func contentRangeStart(v string) (int64, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(v), "bytes ")
	if !ok {
		return 0, false
	}
	start, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(start), 10, 64)
	return n, err == nil
}

// verifyFile checks that path is size bytes long with the given SHA-256.
func verifyFile(ctx context.Context, path string, size int64, sha string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Size() != size {
		return fmt.Errorf("%w: %d bytes, want %d", ErrChecksum, fi.Size(), size)
	}
	got, err := hashFile(ctx, path)
	if err != nil {
		return err
	}
	if got != sha {
		return fmt.Errorf("%w: sha256 %s, want %s", ErrChecksum, got, sha)
	}
	return nil
}

// hashFile returns the hex SHA-256 of the file at path, stopping early when
// ctx is cancelled.
func hashFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if err == io.EOF {
			return hex.EncodeToString(h.Sum(nil)), nil
		}
		if err != nil {
			return "", err
		}
	}
}

// fileSize returns the size of the file at path, or 0 if it does not exist.
func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return 0
	}
	return fi.Size()
}

// syncDir flushes a directory entry change, such as a rename, to disk. It is
// best effort: some platforms and file systems cannot sync directories.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if f, err := os.Open(dir); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
}
