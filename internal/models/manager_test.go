package models

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type managerSetup struct {
	testFiles
	hub *hub
	m   *Manager
	dir string
	hf  string
}

func newManager(t *testing.T) managerSetup {
	t.Helper()
	tf := newTestCatalog(t)
	h := newHub(t)
	tf.serveAll(h)
	root := t.TempDir()
	s := managerSetup{testFiles: tf, hub: h, dir: filepath.Join(root, "models"), hf: filepath.Join(root, "hf", "hub")}
	s.m = NewManager(s.dir, Options{Catalog: tf.cat, HFCache: s.hf, Endpoint: h.srv.URL, Downloader: fastDownloader()})
	return s
}

func (s managerSetup) state(t *testing.T, id, quant string) Status {
	t.Helper()
	st, err := s.m.Status(id, quant)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestFreshInstallHasNoModelAndMakesNoRequest(t *testing.T) {
	tf := newTestCatalog(t)
	root := t.TempDir()
	dir := filepath.Join(root, "models")
	m := NewManager(dir, Options{
		Catalog:    tf.cat,
		HFCache:    filepath.Join(root, "hf"),
		Downloader: &Downloader{Client: &http.Client{Transport: noNetwork{t}}},
	})
	for _, st := range m.List() {
		if st.State != NotDownloaded || st.Path != "" {
			t.Errorf("%s %s: %v %q", st.Model.ID, st.File.Quant, st.State, st.Path)
		}
	}
	if m.Any() {
		t.Error("Any() = true on a fresh install")
	}
	for _, id := range []string{"stream", "batch", "vad"} {
		if _, err := m.Path(id, ""); !errors.Is(err, ErrNotDownloaded) {
			t.Errorf("Path(%s) err = %v, want ErrNotDownloaded", id, err)
		}
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("listing created the models directory: %v", err)
	}
}

func TestDefaultManagerMakesNoRequest(t *testing.T) {
	// The real catalog, a fresh directory and an empty HF cache.
	m := NewManager(t.TempDir(), Options{HFCache: t.TempDir(), Downloader: &Downloader{Client: &http.Client{Transport: noNetwork{t}}}})
	if m.Any() || len(m.List()) != 4 {
		t.Errorf("Any() = %t, %d statuses", m.Any(), len(m.List()))
	}
}

func TestDownloadFetchesTheVADWithTheFirstModel(t *testing.T) {
	s := newManager(t)
	progress := make(chan Progress, 1000)
	if err := s.m.Download(context.Background(), "stream", "", progress); err != nil {
		t.Fatal(err)
	}
	close(progress)

	var last Progress
	sawVAD, sawVerify := false, false
	for p := range progress {
		if p.Model != "stream" || p.Quant != "Q8_0" || p.Total != 310_000 {
			t.Fatalf("progress %+v", p)
		}
		if p.Done < last.Done {
			t.Errorf("progress went back from %d to %d", last.Done, p.Done)
		}
		sawVAD = sawVAD || p.File == "vad.gguf"
		sawVerify = sawVerify || p.Verifying
		last = p
	}
	if last.Done != last.Total || !sawVAD || !sawVerify {
		t.Errorf("last progress %+v, saw VAD %t, saw verifying %t", last, sawVAD, sawVerify)
	}

	for _, id := range []string{"stream", "vad"} {
		p, err := s.m.Path(id, "")
		if err != nil || filepath.Dir(p) != s.dir {
			t.Errorf("Path(%s) = %q, %v", id, p, err)
		}
	}
	if st := s.state(t, "stream", "F16"); st.State != NotDownloaded {
		t.Errorf("F16 is %v; only the requested quantisation downloads", st.State)
	}
	if st := s.state(t, "batch", ""); st.State != NotDownloaded {
		t.Errorf("batch is %v", st.State)
	}
	if !s.m.Any() {
		t.Error("Any() = false after a download")
	}

	// Everything is there now: no more requests.
	n := len(s.hub.requests())
	if err := s.m.Download(context.Background(), "stream", "Q8_0", nil); err != nil {
		t.Fatal(err)
	}
	// The second model needs only its own file.
	if err := s.m.Download(context.Background(), "batch", "", nil); err != nil {
		t.Fatal(err)
	}
	if reqs := s.hub.requests()[n:]; len(reqs) != 1 || filepath.Base(reqs[0].Path) != "batch.q8_0.gguf" {
		t.Errorf("requests after the first download: %+v", reqs)
	}
}

func TestDownloadVADAlone(t *testing.T) {
	s := newManager(t)
	if err := s.m.Download(context.Background(), "vad", "", nil); err != nil {
		t.Fatal(err)
	}
	if s.m.Any() {
		t.Error("Any() = true with only the VAD")
	}
	if reqs := s.hub.requests(); len(reqs) != 1 {
		t.Errorf("requests %+v", reqs)
	}
}

func TestDownloadUnknown(t *testing.T) {
	s := newManager(t)
	if err := s.m.Download(context.Background(), "whisper", "", nil); !errors.Is(err, ErrUnknownModel) {
		t.Errorf("err = %v", err)
	}
	if err := s.m.Download(context.Background(), "stream", "Q2_K", nil); !errors.Is(err, ErrUnknownQuant) {
		t.Errorf("err = %v", err)
	}
	if len(s.hub.requests()) != 0 {
		t.Error("requests made")
	}
}

func TestDownloadChecksFreeSpaceFirst(t *testing.T) {
	s := newManager(t)
	s.m.freeSpace = func(string) (uint64, error) { return 1 << 20, nil }
	err := s.m.Download(context.Background(), "stream", "", nil)
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("err = %v, want ErrNoSpace", err)
	}
	if len(s.hub.requests()) != 0 {
		t.Error("requests made without the space for them")
	}
	if st := s.state(t, "stream", ""); st.State != NotDownloaded {
		t.Errorf("state %v", st.State)
	}

	// A partial download only needs the rest.
	var asked uint64
	s.m.freeSpace = func(string) (uint64, error) { return 1 << 40, nil }
	_ = os.MkdirAll(s.dir, 0o700)
	_ = os.WriteFile(filepath.Join(s.dir, "stream.q8_0.gguf.part"), s.data["stream.q8_0.gguf"][:100_000], 0o600)
	s.m.freeSpace = func(string) (uint64, error) { asked++; return 210_000 + spaceHeadroom, nil }
	if err := s.m.Download(context.Background(), "stream", "", nil); err != nil {
		t.Fatalf("partial download with room for the rest: %v", err)
	}
	if asked != 1 {
		t.Errorf("free space checked %d times", asked)
	}
}

func TestStatusOfPartialAndCorruptFiles(t *testing.T) {
	s := newManager(t)
	_ = os.MkdirAll(s.dir, 0o700)
	_ = os.WriteFile(filepath.Join(s.dir, "stream.q8_0.gguf.part"), make([]byte, 30_000), 0o600)
	if st := s.state(t, "stream", ""); st.State != NotDownloaded || st.Done != 30_000 || st.Percent() != 10 {
		t.Errorf("partial: %v, %d bytes, %d%%", st.State, st.Done, st.Percent())
	}

	// Right size, wrong bytes.
	bad := filepath.Join(s.dir, "batch.q8_0.gguf")
	_ = os.WriteFile(bad, make([]byte, 200_000), 0o600)
	if st := s.state(t, "batch", ""); st.State != Corrupt || st.Path != bad {
		t.Errorf("corrupt: %v %q", st.State, st.Path)
	}
	if _, err := s.m.Path("batch", ""); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Path err = %v, want ErrCorrupt", err)
	}
	// Wrong size.
	_ = os.WriteFile(bad, []byte("short"), 0o600)
	if st := s.state(t, "batch", ""); st.State != Corrupt {
		t.Errorf("short file: %v", st.State)
	}
	// Download replaces it.
	if err := s.m.Download(context.Background(), "batch", "", nil); err != nil {
		t.Fatal(err)
	}
	if st := s.state(t, "batch", ""); st.State != Ready || st.Path != bad {
		t.Errorf("after download: %v %q", st.State, st.Path)
	}
}

func TestDownloadingStateAndBusy(t *testing.T) {
	s := newManager(t)
	release := make(chan struct{})
	data := s.data["stream.q8_0.gguf"]
	s.hub.setFault(func(_ int, w http.ResponseWriter, r *http.Request) bool {
		if filepath.Base(r.URL.Path) != "stream.q8_0.gguf" {
			return false
		}
		w.Header().Set("Content-Length", "300000")
		_, _ = w.Write(data[:150_000])
		w.(http.Flusher).Flush()
		<-release
		_, _ = w.Write(data[150_000:])
		return true
	})
	done := make(chan error, 1)
	go func() { done <- s.m.Download(context.Background(), "stream", "", nil) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		st := s.state(t, "stream", "")
		if st.State == Downloading && st.Done == 150_000 {
			if st.Percent() != 50 {
				t.Errorf("percent %d", st.Percent())
			}
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatalf("state %v, %d bytes", st.State, st.Done)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.m.Download(context.Background(), "stream", "", nil); !errors.Is(err, ErrBusy) {
		t.Errorf("second Download err = %v, want ErrBusy", err)
	}
	if err := s.m.Delete("stream"); !errors.Is(err, ErrBusy) {
		t.Errorf("Delete err = %v, want ErrBusy", err)
	}
	if _, err := s.m.Path("stream", ""); !errors.Is(err, ErrBusy) {
		t.Errorf("Path err = %v, want ErrBusy", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st := s.state(t, "stream", ""); st.State != Ready {
		t.Errorf("after download: %v", st.State)
	}
}

func TestDownloadCancelKeepsThePartialFile(t *testing.T) {
	s := newManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	data := s.data["stream.q8_0.gguf"]
	s.hub.setFault(func(_ int, w http.ResponseWriter, r *http.Request) bool {
		if filepath.Base(r.URL.Path) != "stream.q8_0.gguf" {
			return false
		}
		w.Header().Set("Content-Length", "300000")
		_, _ = w.Write(data[:120_000])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return true
	})
	done := make(chan error, 1)
	go func() { done <- s.m.Download(ctx, "stream", "", nil) }()
	// Cancel once the 120 kB sent are on disk.
	for deadline := time.Now().Add(10 * time.Second); s.state(t, "stream", "").Done < 120_000; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the first 120 kB never arrived")
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	st := s.state(t, "stream", "")
	if st.State != NotDownloaded || st.Done != 120_000 {
		t.Errorf("after cancel: %v with %d bytes", st.State, st.Done)
	}
	if v := s.state(t, "vad", ""); v.State != Ready {
		t.Errorf("the VAD, fetched first, is %v", v.State)
	}
	s.hub.setFault(nil)
	if err := s.m.Download(context.Background(), "stream", "", nil); err != nil {
		t.Fatal(err)
	}
	reqs := s.hub.requests()
	if last := reqs[len(reqs)-1]; last.Range != "bytes=120000-" {
		t.Errorf("resumed with %q", last.Range)
	}
}

func TestDelete(t *testing.T) {
	s := newManager(t)
	if err := s.m.Delete("stream"); !errors.Is(err, ErrNotDownloaded) {
		t.Errorf("deleting nothing: %v", err)
	}
	if err := s.m.Delete("nope"); !errors.Is(err, ErrUnknownModel) {
		t.Errorf("unknown: %v", err)
	}
	if err := s.m.Download(context.Background(), "stream", "", nil); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(s.dir, "stream.f16.gguf.part"), []byte("partial"), 0o600)
	if err := s.m.Delete("stream"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stream.q8_0.gguf", "stream.f16.gguf.part"} {
		if _, err := os.Stat(filepath.Join(s.dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived: %v", name, err)
		}
	}
	if s.state(t, "vad", "").State != Ready {
		t.Error("deleting a model deleted the VAD")
	}
	if s.m.Any() {
		t.Error("Any() = true after deleting the only model")
	}
}

// hfStore puts data into the hub cache the way huggingface_hub does: a blob
// named by its SHA-256 and a snapshot entry linking to it.
func hfStore(t *testing.T, cache, repo, rev, name string, data []byte) string {
	t.Helper()
	repoDir := filepath.Join(cache, "models--"+strings.ReplaceAll(repo, "/", "--"))
	blob := filepath.Join(repoDir, "blobs", sum(data))
	snap := filepath.Join(repoDir, "snapshots", rev, name)
	for _, d := range []string{filepath.Dir(blob), filepath.Dir(snap)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(blob, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "blobs", sum(data)), snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestHFCacheFileIsReadyButNotManaged(t *testing.T) {
	s := newManager(t)
	// Any snapshot will do: the checksum decides.
	snap := hfStore(t, s.hf, "org/batch", "0123456789abcdef0123456789abcdef01234567", "batch.q8_0.gguf", s.data["batch.q8_0.gguf"])

	st := s.state(t, "batch", "")
	if st.State != Ready || !st.FromHFCache || st.Path != snap {
		t.Fatalf("status %v from HF %t at %q", st.State, st.FromHFCache, st.Path)
	}
	if !s.m.Any() {
		t.Error("Any() = false with a verified model in the HF cache")
	}
	if p, err := s.m.Path("batch", ""); err != nil || p != snap {
		t.Errorf("Path = %q, %v", p, err)
	}
	// Download fetches only the VAD.
	if err := s.m.Download(context.Background(), "batch", "", nil); err != nil {
		t.Fatal(err)
	}
	if reqs := s.hub.requests(); len(reqs) != 1 || filepath.Base(reqs[0].Path) != "vad.gguf" {
		t.Errorf("requests %+v", reqs)
	}
	// Delete leaves the HF cache alone.
	if err := s.m.Delete("batch"); !errors.Is(err, ErrNotDownloaded) {
		t.Errorf("Delete err = %v", err)
	}
	if _, err := os.Stat(snap); err != nil {
		t.Errorf("the HF cache copy is gone: %v", err)
	}
}

func TestHFCacheIgnoresWrongFiles(t *testing.T) {
	s := newManager(t)
	rev := s.cat.Models[1].Revision
	// Same name, other bytes, in the pinned snapshot.
	hfStore(t, s.hf, "org/batch", rev, "batch.q8_0.gguf", payload(77, 200_000))
	// A plain file of the right size but wrong bytes in another snapshot.
	other := filepath.Join(s.hf, "models--org--batch", "snapshots", "other", "batch.q8_0.gguf")
	_ = os.MkdirAll(filepath.Dir(other), 0o755)
	_ = os.WriteFile(other, make([]byte, 200_000), 0o644)

	if st := s.state(t, "batch", ""); st.State != NotDownloaded {
		t.Errorf("state %v with only wrong files in the HF cache", st.State)
	}
}

func TestHFCandidateOrder(t *testing.T) {
	dir := t.TempDir()
	data := payload(5, 1000)
	pinned := strings.Repeat("1", 40)
	mainRev := strings.Repeat("2", 40)
	for _, rev := range []string{"zzz", mainRev, pinned} {
		hfStore(t, dir, "org/m", rev, "m.gguf", data)
	}
	refs := filepath.Join(dir, "models--org--m", "refs")
	_ = os.MkdirAll(refs, 0o755)
	_ = os.WriteFile(filepath.Join(refs, "main"), []byte(mainRev+"\n"), 0o644)

	got := hfCandidates(dir, "org/m", pinned, "m.gguf", sum(data))
	snaps := filepath.Join(dir, "models--org--m", "snapshots")
	want := []string{filepath.Join(snaps, pinned, "m.gguf"), filepath.Join(snaps, mainRev, "m.gguf"), filepath.Join(snaps, "zzz", "m.gguf")}
	if len(got) != len(want) {
		t.Fatalf("candidates %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d: %s, want %s", i, got[i], want[i])
		}
	}
	if c := hfCandidates(dir, "org/m", pinned, "m.gguf", sum([]byte("other"))); len(c) != 0 {
		t.Errorf("blobs named by another SHA-256 were not skipped: %v", c)
	}
	if c := hfCandidates("", "org/m", pinned, "m.gguf", sum(data)); c != nil {
		t.Errorf("no cache dir: %v", c)
	}
}

func TestHFCacheDir(t *testing.T) {
	for _, tt := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"HF_HUB_CACHE": "/x/hub", "HF_HOME": "/y"}, "/x/hub"},
		{map[string]string{"HUGGINGFACE_HUB_CACHE": "/old"}, "/old"},
		{map[string]string{"HF_HOME": "/y", "XDG_CACHE_HOME": "/z"}, "/y/hub"},
		{map[string]string{"HF_HOME": "~/hf", "HOME": "/home/u"}, "/home/u/hf/hub"},
		{map[string]string{"XDG_CACHE_HOME": "/z"}, "/z/huggingface/hub"},
		{map[string]string{"HOME": "/home/u"}, "/home/u/.cache/huggingface/hub"},
	} {
		got := HFCacheDir(func(k string) string { return tt.env[k] })
		if got != filepath.FromSlash(tt.want) {
			t.Errorf("%v: %q, want %q", tt.env, got, tt.want)
		}
	}
}

func TestEndpointFromEnv(t *testing.T) {
	if got := EndpointFromEnv(func(string) string { return "" }); got != DefaultEndpoint {
		t.Errorf("default %q", got)
	}
	if got := EndpointFromEnv(func(string) string { return "https://mirror.example/" }); got != "https://mirror.example" {
		t.Errorf("override %q", got)
	}
}

func TestVerifierHashesOnlyChangedFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	data := payload(1, 4096)
	_ = os.WriteFile(p, data, 0o600)
	v := &verifier{path: filepath.Join(dir, verifiedFile)}
	if ok, err := v.check(context.Background(), p, 4096, sum(data)); !ok || err != nil {
		t.Fatalf("check = %t, %v", ok, err)
	}

	// Same size and time, other bytes: the record is trusted, also by a
	// verifier that loads it from disk.
	fi, _ := os.Stat(p)
	_ = os.WriteFile(p, payload(2, 4096), 0o600)
	_ = os.Chtimes(p, fi.ModTime(), fi.ModTime())
	reloaded := &verifier{path: v.path}
	if ok, _ := reloaded.check(context.Background(), p, 4096, sum(data)); !ok {
		t.Error("an unchanged file was hashed again")
	}

	// A new modification time means a new hash.
	later := fi.ModTime().Add(time.Second)
	_ = os.Chtimes(p, later, later)
	if ok, _ := reloaded.check(context.Background(), p, 4096, sum(data)); ok {
		t.Error("a changed file passed on its old record")
	}
	if ok, err := reloaded.check(context.Background(), p, 1, sum(data)); ok || err != nil {
		t.Errorf("wrong size: %t, %v", ok, err)
	}
}
