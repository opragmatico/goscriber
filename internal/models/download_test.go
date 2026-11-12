package models

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// fetchSetup serves one file and returns a Target for it in a fresh
// directory.
func fetchSetup(t *testing.T, n int) (*hub, Target, []byte) {
	t.Helper()
	h := newHub(t)
	data := payload(9, n)
	m := &Model{Repo: "org/repo", Revision: "rev"}
	f := File{Name: "model.gguf", Size: int64(n), SHA256: sum(data)}
	h.add(m, f, data)
	return h, Target{
		URL:    m.URL(h.srv.URL, f),
		Dest:   filepath.Join(t.TempDir(), "models", f.Name),
		Size:   f.Size,
		SHA256: f.SHA256,
	}, data
}

func checkDownloaded(t *testing.T, tg Target, want []byte) {
	t.Helper()
	got, err := os.ReadFile(tg.Dest)
	if err != nil {
		t.Fatalf("destination: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("destination holds %d bytes that differ from the %d served", len(got), len(want))
	}
	if _, err := os.Stat(tg.Dest + partSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("partial file left behind: %v", err)
	}
}

func TestFetch(t *testing.T) {
	h, tg, data := fetchSetup(t, 600_000)
	var last int64
	verified := false
	if err := fastDownloader().Fetch(context.Background(), tg, func(n int64) { last = n }, func() { verified = true }); err != nil {
		t.Fatal(err)
	}
	checkDownloaded(t, tg, data)
	if last != tg.Size || !verified {
		t.Errorf("last progress %d of %d, verifying reported %t", last, tg.Size, verified)
	}
	if reqs := h.requests(); len(reqs) != 1 || reqs[0].Range != "" {
		t.Errorf("requests %+v, want one without Range", reqs)
	}
	if fi, _ := os.Stat(tg.Dest); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestFetchResumesAfterInterruption(t *testing.T) {
	h, tg, data := fetchSetup(t, 600_000)
	h.setFault(func(n int, w http.ResponseWriter, r *http.Request) bool {
		if n < 2 {
			truncate(w, r, data, 150_000) // drop the connection after 150 kB, twice
			return true
		}
		return false
	})
	if err := fastDownloader().Fetch(context.Background(), tg, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkDownloaded(t, tg, data)
	want := []hubRequest{{Range: ""}, {Range: "bytes=150000-"}, {Range: "bytes=300000-"}}
	reqs := h.requests()
	if len(reqs) != len(want) {
		t.Fatalf("requests %+v, want ranges %+v", reqs, want)
	}
	for i := range want {
		if reqs[i].Range != want[i].Range {
			t.Errorf("request %d: Range %q, want %q", i, reqs[i].Range, want[i].Range)
		}
	}
}

func TestFetchResumesAcrossRuns(t *testing.T) {
	// The process quits mid-download (here: cancels), then a later Fetch
	// picks up the partial file.
	h, tg, data := fetchSetup(t, 600_000)
	ctx, cancel := context.WithCancel(context.Background())
	h.setFault(func(n int, w http.ResponseWriter, r *http.Request) bool {
		if n == 0 {
			truncate(w, r, data, 200_000)
			return true
		}
		return false
	})
	dl := &Downloader{Backoff: func(int) time.Duration { cancel(); return time.Hour }}
	err := dl.Fetch(ctx, tg, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := fileSize(tg.Dest + partSuffix); got != 200_000 {
		t.Fatalf("partial file has %d bytes, want 200000", got)
	}
	if _, err := os.Stat(tg.Dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination exists after a cancelled download: %v", err)
	}

	if err := fastDownloader().Fetch(context.Background(), tg, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkDownloaded(t, tg, data)
	if reqs := h.requests(); reqs[len(reqs)-1].Range != "bytes=200000-" {
		t.Errorf("second run asked for %q, want bytes=200000-", reqs[len(reqs)-1].Range)
	}
}

func TestFetchCancel(t *testing.T) {
	h, tg, data := fetchSetup(t, 600_000)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h.setFault(func(_ int, w http.ResponseWriter, _ *http.Request) bool {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data[:100_000])
		w.(http.Flusher).Flush()
		<-release // hold the connection open
		return true
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- fastDownloader().Fetch(ctx, tg, func(n int64) {
			if n >= 100_000 {
				cancel()
			}
		}, nil)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Fetch did not return after cancel")
	}
	if got := fileSize(tg.Dest + partSuffix); got != 100_000 {
		t.Errorf("partial file has %d bytes, want the 100000 received", got)
	}
	if _, err := os.Stat(tg.Dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("destination exists after cancel: %v", err)
	}
}

func TestFetchChecksumMismatch(t *testing.T) {
	h, tg, _ := fetchSetup(t, 300_000)
	tg.SHA256 = sum([]byte("something else"))
	err := fastDownloader().Fetch(context.Background(), tg, nil, nil)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v, want ErrChecksum", err)
	}
	for _, p := range []string{tg.Dest, tg.Dest + partSuffix} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after a checksum mismatch: %v", filepath.Base(p), err)
		}
	}
	if n := len(h.requests()); n != 1 {
		t.Errorf("%d requests; a fresh download that fails its checksum is not retried", n)
	}
}

func TestFetchRestartsOnceWhenAResumedFileIsCorrupt(t *testing.T) {
	h, tg, data := fetchSetup(t, 300_000)
	if err := os.MkdirAll(filepath.Dir(tg.Dest), 0o700); err != nil {
		t.Fatal(err)
	}
	// A partial file from an earlier run, with the wrong bytes.
	if err := os.WriteFile(tg.Dest+partSuffix, payload(99, 100_000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fastDownloader().Fetch(context.Background(), tg, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkDownloaded(t, tg, data)
	reqs := h.requests()
	if len(reqs) != 2 || reqs[0].Range != "bytes=100000-" || reqs[1].Range != "" {
		t.Errorf("requests %+v, want a resume and then a fresh download", reqs)
	}
}

func TestFetchServerIgnoresRange(t *testing.T) {
	h, tg, data := fetchSetup(t, 300_000)
	_ = os.MkdirAll(filepath.Dir(tg.Dest), 0o700)
	_ = os.WriteFile(tg.Dest+partSuffix, data[:50_000], 0o600)
	h.setFault(func(_ int, w http.ResponseWriter, _ *http.Request) bool {
		_, _ = w.Write(data) // 200 with the whole file
		return true
	})
	if err := fastDownloader().Fetch(context.Background(), tg, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkDownloaded(t, tg, data)
}

func TestFetchWrongContentRange(t *testing.T) {
	h, tg, data := fetchSetup(t, 300_000)
	_ = os.MkdirAll(filepath.Dir(tg.Dest), 0o700)
	_ = os.WriteFile(tg.Dest+partSuffix, data[:50_000], 0o600)
	h.setFault(func(n int, w http.ResponseWriter, _ *http.Request) bool {
		if n > 0 {
			return false
		}
		w.Header().Set("Content-Range", "bytes 0-299999/300000")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data)
		return true
	})
	if err := fastDownloader().Fetch(context.Background(), tg, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkDownloaded(t, tg, data)
	if reqs := h.requests(); len(reqs) != 2 || reqs[1].Range != "" {
		t.Errorf("requests %+v, want the partial file dropped and a fresh download", reqs)
	}
}

func TestFetchOversizedOrUnsatisfiablePartial(t *testing.T) {
	for _, tt := range []struct {
		name string
		part []byte
	}{
		{"larger than the file", payload(1, 400_000)},
		{"range not satisfiable", nil}, // the server answers 416
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, tg, data := fetchSetup(t, 300_000)
			_ = os.MkdirAll(filepath.Dir(tg.Dest), 0o700)
			part := tt.part
			if part == nil {
				part = data[:10]
				h.setFault(func(n int, w http.ResponseWriter, _ *http.Request) bool {
					if n == 0 {
						w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
						return true
					}
					return false
				})
			}
			_ = os.WriteFile(tg.Dest+partSuffix, part, 0o600)
			if err := fastDownloader().Fetch(context.Background(), tg, nil, nil); err != nil {
				t.Fatal(err)
			}
			checkDownloaded(t, tg, data)
		})
	}
}

func TestFetchCompletePartialIsOnlyVerified(t *testing.T) {
	h, tg, data := fetchSetup(t, 300_000)
	_ = os.MkdirAll(filepath.Dir(tg.Dest), 0o700)
	_ = os.WriteFile(tg.Dest+partSuffix, data, 0o600)
	if err := fastDownloader().Fetch(context.Background(), tg, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkDownloaded(t, tg, data)
	if n := len(h.requests()); n != 0 {
		t.Errorf("%d requests for a file that was already complete", n)
	}
}

func TestFetchRetriesTransientErrors(t *testing.T) {
	h, tg, data := fetchSetup(t, 300_000)
	h.setFault(func(n int, w http.ResponseWriter, _ *http.Request) bool {
		switch n {
		case 0:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			return false
		}
		return true
	})
	var waits []int
	dl := &Downloader{Backoff: func(n int) time.Duration { waits = append(waits, n); return 0 }}
	if err := dl.Fetch(context.Background(), tg, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkDownloaded(t, tg, data)
	if len(waits) != 2 || waits[0] != 1 || waits[1] != 2 {
		t.Errorf("backoff calls %v, want [1 2]", waits)
	}
}

func TestFetchGivesUp(t *testing.T) {
	h, tg, _ := fetchSetup(t, 1000)
	h.setFault(func(_ int, w http.ResponseWriter, _ *http.Request) bool {
		w.WriteHeader(http.StatusBadGateway)
		return true
	})
	dl := &Downloader{Attempts: 3, Backoff: func(int) time.Duration { return 0 }}
	err := dl.Fetch(context.Background(), tg, nil, nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusBadGateway {
		t.Fatalf("err = %v, want a 502 StatusError", err)
	}
	if n := len(h.requests()); n != 3 {
		t.Errorf("%d requests, want 3 attempts", n)
	}
}

func TestFetchDoesNotRetryNotFound(t *testing.T) {
	h, tg, _ := fetchSetup(t, 1000)
	tg.URL += "-missing"
	err := fastDownloader().Fetch(context.Background(), tg, nil, nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusNotFound {
		t.Fatalf("err = %v, want a 404 StatusError", err)
	}
	if n := len(h.requests()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestFetchRetriesAStall(t *testing.T) {
	h, tg, data := fetchSetup(t, 300_000)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var stalled atomic.Bool
	h.setFault(func(n int, w http.ResponseWriter, _ *http.Request) bool {
		if n > 0 {
			return false
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data[:1000])
		w.(http.Flusher).Flush()
		stalled.Store(true)
		<-release
		return true
	})
	dl := &Downloader{StallTimeout: 200 * time.Millisecond, Backoff: func(int) time.Duration { return 0 }}
	if err := dl.Fetch(context.Background(), tg, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkDownloaded(t, tg, data)
	if reqs := h.requests(); !stalled.Load() || len(reqs) != 2 || reqs[1].Range != "bytes=1000-" {
		t.Errorf("requests %+v, want the stalled one resumed at byte 1000", reqs)
	}
}

func TestFetchServerSendsTooMuch(t *testing.T) {
	h, tg, data := fetchSetup(t, 1000)
	h.setFault(func(_ int, w http.ResponseWriter, _ *http.Request) bool {
		_, _ = w.Write(append(data, 1, 2, 3))
		return true
	})
	err := fastDownloader().Fetch(context.Background(), tg, nil, nil)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v, want ErrChecksum", err)
	}
	if _, err := os.Stat(tg.Dest + partSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("partial file kept: %v", err)
	}
}

func TestContentRangeStart(t *testing.T) {
	for in, want := range map[string]int64{"bytes 100-199/200": 100, " bytes 0-0/1": 0} {
		if got, ok := contentRangeStart(in); !ok || got != want {
			t.Errorf("contentRangeStart(%q) = %d, %t", in, got, ok)
		}
	}
	for _, in := range []string{"", "bytes */200", "items 1-2/3", "bytes x-1/2"} {
		if _, ok := contentRangeStart(in); ok {
			t.Errorf("contentRangeStart(%q) accepted", in)
		}
	}
}
