package models

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// payload returns n deterministic bytes that differ per seed.
func payload(seed byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7) ^ seed
	}
	return b
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// hub is a fake Hugging Face endpoint: it serves files at
// /{repo}/resolve/{revision}/{file} with Range support, and fault, when set,
// may take over any request.
type hub struct {
	srv   *httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	reqs  []hubRequest
	fault func(n int, w http.ResponseWriter, r *http.Request) bool
}

type hubRequest struct {
	Path  string
	Range string
}

func newHub(t *testing.T) *hub {
	t.Helper()
	h := &hub{files: map[string][]byte{}}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		n := len(h.reqs)
		h.reqs = append(h.reqs, hubRequest{Path: r.URL.Path, Range: r.Header.Get("Range")})
		data, ok := h.files[r.URL.Path]
		fault := h.fault
		h.mu.Unlock()
		if fault != nil && fault(n, w, r) {
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hub) add(m *Model, f File, data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[m.URL("", f)] = data // "/{repo}/resolve/{revision}/{file}"
}

func (h *hub) requests() []hubRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hubRequest(nil), h.reqs...)
}

func (h *hub) setFault(f func(n int, w http.ResponseWriter, r *http.Request) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fault = f
}

// truncate answers with the full Content-Length but only the first n bytes
// of data after the requested offset, then drops the connection.
func truncate(w http.ResponseWriter, r *http.Request, data []byte, n int) {
	off := 0
	status := http.StatusOK
	if rg, ok := strings.CutPrefix(r.Header.Get("Range"), "bytes="); ok {
		off, _ = strconv.Atoi(strings.TrimSuffix(rg, "-"))
		status = http.StatusPartialContent
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, len(data)-1, len(data)))
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)-off))
	w.WriteHeader(status)
	_, _ = w.Write(data[off : off+n])
	w.(http.Flusher).Flush()
	panic(http.ErrAbortHandler)
}

// testFiles is a catalog with two ASR models and the VAD, with small files whose
// contents payload returns.
type testFiles struct {
	cat  *Catalog
	data map[string][]byte // by file name
}

func newTestCatalog(t *testing.T) testFiles {
	t.Helper()
	tf := testFiles{data: map[string][]byte{}}
	file := func(quant, name string, seed byte, n int) File {
		b := payload(seed, n)
		tf.data[name] = b
		return File{Quant: quant, Name: name, Size: int64(n), SHA256: sum(b)}
	}
	rev := strings.Repeat("a", 40)
	tf.cat = &Catalog{
		DefaultModel: "stream",
		Models: []Model{
			{
				ID: "stream", Kind: KindASR, Name: "Streaming", Repo: "org/stream", Revision: rev, License: "MIT",
				Languages: []string{"en-US"}, Capabilities: Capabilities{Streaming: true}, DefaultQuant: "Q8_0",
				Files: []File{file("Q8_0", "stream.q8_0.gguf", 1, 300_000), file("F16", "stream.f16.gguf", 2, 500_000)},
			},
			{
				ID: "batch", Kind: KindASR, Name: "Batch", Repo: "org/batch", Revision: rev, License: "MIT",
				Languages: []string{"en"}, DefaultQuant: "Q8_0",
				Files: []File{file("Q8_0", "batch.q8_0.gguf", 3, 200_000)},
			},
			{
				ID: "vad", Kind: KindVAD, Name: "VAD", Repo: "org/vad", Revision: rev, License: "MIT", DefaultQuant: "F32",
				Files: []File{file("F32", "vad.gguf", 4, 10_000)},
			},
		},
	}
	if err := tf.cat.validate(); err != nil {
		t.Fatal(err)
	}
	return tf
}

// serveAll puts every catalog file on the hub.
func (tf testFiles) serveAll(h *hub) {
	for i := range tf.cat.Models {
		m := &tf.cat.Models[i]
		for _, f := range m.Files {
			h.add(m, f, tf.data[f.Name])
		}
	}
}

// noNetwork is a transport that fails the test on any request.
type noNetwork struct{ t *testing.T }

func (n noNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	n.t.Errorf("unexpected request: %s %s", r.Method, r.URL)
	return nil, http.ErrNotSupported
}

func fastDownloader() *Downloader {
	return &Downloader{Backoff: func(int) time.Duration { return time.Millisecond }}
}
