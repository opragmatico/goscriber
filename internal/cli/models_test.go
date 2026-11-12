package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/models"
)

// fakeModels is a two-model catalog served by a local fake Hugging Face.
type fakeModels struct {
	m        *models.Manager
	dir      string
	hfCache  string
	requests atomic.Int32
	files    map[string][]byte
}

func newFakeModels(t *testing.T) *fakeModels {
	t.Helper()
	f := &fakeModels{files: map[string][]byte{
		"asr.q8_0.gguf": bytes.Repeat([]byte("asr-q8 "), 3000),
		"asr.f16.gguf":  bytes.Repeat([]byte("asr-f16 "), 5000),
		"vad.gguf":      bytes.Repeat([]byte("vad "), 100),
	}}
	file := func(quant, name string) string {
		h := sha256.Sum256(f.files[name])
		return fmt.Sprintf(`{"quant": %q, "filename": %q, "size": %d, "sha256": %q}`, quant, name, len(f.files[name]), hex.EncodeToString(h[:]))
	}
	rev := strings.Repeat("c", 40)
	cat, err := models.Parse([]byte(`{"default_model": "asr", "models": [
		{"id": "asr", "kind": "asr", "name": "ASR", "repo": "org/asr", "revision": "` + rev + `", "license": "MIT",
		 "capabilities": {"streaming": true}, "default_quant": "Q8_0",
		 "files": [` + file("Q8_0", "asr.q8_0.gguf") + `, ` + file("F16", "asr.f16.gguf") + `]},
		{"id": "vad", "kind": "vad", "name": "VAD", "repo": "org/vad", "revision": "` + rev + `", "license": "MIT",
		 "capabilities": {}, "default_quant": "F32", "files": [` + file("F32", "vad.gguf") + `]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		data, ok := f.files[filepath.Base(r.URL.Path)]
		if !ok || !strings.Contains(r.URL.Path, "/resolve/"+rev+"/") {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	}))
	t.Cleanup(srv.Close)
	root := t.TempDir()
	f.dir = filepath.Join(root, "models")
	f.hfCache = filepath.Join(root, "hf")
	f.m = models.NewManager(f.dir, models.Options{Catalog: cat, HFCache: f.hfCache, Endpoint: srv.URL})
	return f
}

func runWith(t *testing.T, env Env, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env.Stdout, env.Stderr = &out, &errOut
	code = Run(args, env)
	return code, out.String(), errOut.String()
}

func TestModelsList(t *testing.T) {
	f := newFakeModels(t)
	code, stdout, stderr := runWith(t, Env{Models: f.m}, "models", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, want := range []string{"MODEL", "asr", "Q8_0 (default)", "F16", "vad", "F32", "21 kB", "not downloaded", "Models directory: " + f.dir} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output lacks %q:\n%s", want, stdout)
		}
	}
	if n := f.requests.Load(); n != 0 {
		t.Errorf("list made %d requests", n)
	}
}

func TestModelsPullListRemove(t *testing.T) {
	f := newFakeModels(t)
	env := Env{Models: f.m}

	code, stdout, stderr := runWith(t, env, "models", "pull", "asr")
	if code != 0 {
		t.Fatalf("pull: exit %d, stderr %q", code, stderr)
	}
	if want := "asr Q8_0 is ready: " + filepath.Join(f.dir, "asr.q8_0.gguf"); !strings.Contains(stdout, want) {
		t.Errorf("pull stdout %q, want %q", stdout, want)
	}
	if !strings.Contains(stderr, "downloading vad.gguf") || !strings.Contains(stderr, "downloading asr.q8_0.gguf") {
		t.Errorf("pull progress %q", stderr)
	}
	for _, name := range []string{"asr.q8_0.gguf", "vad.gguf"} {
		if got, _ := os.ReadFile(filepath.Join(f.dir, name)); !bytes.Equal(got, f.files[name]) {
			t.Errorf("%s not downloaded intact", name)
		}
	}

	// Flags may follow the model.
	if code, _, stderr := runWith(t, env, "models", "pull", "asr", "--quant", "f16"); code != 0 {
		t.Fatalf("pull --quant: exit %d, %q", code, stderr)
	}

	_, stdout, _ = runWith(t, env, "models", "list")
	if strings.Count(stdout, "ready") != 3 {
		t.Errorf("after pulls:\n%s", stdout)
	}

	code, stdout, _ = runWith(t, env, "models", "rm", "asr")
	if code != 0 || !strings.Contains(stdout, "removed asr") {
		t.Errorf("rm: exit %d, %q", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "asr.f16.gguf")); !os.IsNotExist(err) {
		t.Errorf("rm left the F16 file: %v", err)
	}
	code, _, stderr = runWith(t, env, "models", "rm", "asr")
	if code != 1 || !strings.Contains(stderr, "is not in") {
		t.Errorf("rm again: exit %d, %q", code, stderr)
	}
}

func TestModelsPullAlreadyInHFCache(t *testing.T) {
	f := newFakeModels(t)
	snap := filepath.Join(f.hfCache, "models--org--asr", "snapshots", "any", "asr.q8_0.gguf")
	_ = os.MkdirAll(filepath.Dir(snap), 0o755)
	if err := os.WriteFile(snap, f.files["asr.q8_0.gguf"], 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runWith(t, Env{Models: f.m}, "models", "pull", "asr")
	if code != 0 || !strings.Contains(stdout, snap+" (Hugging Face cache)") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if n := f.requests.Load(); n != 1 {
		t.Errorf("%d requests; only the VAD should download", n)
	}
	code, _, stderr = runWith(t, Env{Models: f.m}, "models", "rm", "asr")
	if code != 1 || !strings.Contains(stderr, "Hugging Face cache stays: "+snap) {
		t.Errorf("rm: exit %d, %q", code, stderr)
	}
}

func TestModelsErrors(t *testing.T) {
	f := newFakeModels(t)
	env := Env{Models: f.m}
	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"models"}, 2, "Usage:"},
		{[]string{"models", "frobnicate"}, 2, `unknown command "frobnicate"`},
		{[]string{"models", "pull"}, 2, "want one MODEL"},
		{[]string{"models", "pull", "a", "b"}, 2, "want one MODEL"},
		{[]string{"models", "pull", "--nope", "asr"}, 2, "flag provided but not defined"},
		{[]string{"models", "pull", "whisper"}, 1, `unknown model "whisper"`},
		{[]string{"models", "pull", "asr", "--quant", "Q2_K"}, 1, `unknown quantisation "Q2_K"`},
		{[]string{"models", "rm"}, 2, "want one MODEL"},
		{[]string{"models", "list", "extra"}, 2, "unexpected argument"},
	} {
		code, stdout, stderr := runWith(t, env, tt.args...)
		if code != tt.code || !strings.Contains(stderr, tt.want) || stdout != "" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want exit %d and %q", tt.args, code, stdout, stderr, tt.code, tt.want)
		}
	}
	if n := f.requests.Load(); n != 0 {
		t.Errorf("%d requests made by failing commands", n)
	}
	code, stdout, _ := runWith(t, env, "models", "--help")
	if code != 0 || !strings.Contains(stdout, "goscriber models pull") {
		t.Errorf("models --help: exit %d, %q", code, stdout)
	}
	code, stdout, _ = runWith(t, env, "models", "pull", "-h")
	if code != 0 || !strings.Contains(stdout, "--quant") {
		t.Errorf("models pull -h: exit %d, %q", code, stdout)
	}
}

func TestModelsUsesGoscriberHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOSCRIBER_HOME", home)
	t.Setenv("HF_HUB_CACHE", filepath.Join(home, "hf"))
	code, stdout, stderr := runWith(t, Env{}, "models", "list")
	if code != 0 || !strings.Contains(stdout, "nemotron-3.5-asr-streaming-0.6b") || !strings.Contains(stdout, filepath.Join(home, "models")) {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}
