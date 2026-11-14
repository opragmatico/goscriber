package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/models"
)

// testCatalog has a multilingual streaming model in two quantisations, an
// offline English-only one and the VAD. Each file holds its own name, so its size and checksum
// are known.
func testCatalog(t *testing.T) *models.Catalog {
	t.Helper()
	file := func(quant, name string) string {
		h := sha256.Sum256([]byte(name))
		return fmt.Sprintf(`{"quant": %q, "filename": %q, "size": %d, "sha256": %q}`, quant, name, len(name), hex.EncodeToString(h[:]))
	}
	model := func(id, kind, langs string, detect bool, files ...string) string {
		streaming := detect
		list := files[0]
		for _, f := range files[1:] {
			list += ", " + f
		}
		return fmt.Sprintf(`{"id": %q, "kind": %q, "name": "Model %s", "repo": "org/%s", "revision": "0123456789abcdef0123456789abcdef01234567",
			"license": "MIT", "languages": [%s], "capabilities": {"language_detection": %v, "streaming": %v}, "default_quant": "Q8_0", "files": [%s]}`,
			id, kind, id, id, langs, detect, streaming, list)
	}
	cat, err := models.Parse([]byte(`{"default_model": "multi", "models": [` +
		model("multi", "asr", `"en-US", "de-DE"`, true, file("Q8_0", "multi.q8_0.gguf"), file("F16", "multi.f16.gguf")) + ", " +
		model("english", "asr", `"en"`, false, file("Q8_0", "english.gguf")) + ", " +
		model("vad", "vad", "", false, file("Q8_0", "vad.gguf")) +
		`]}`))
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func put(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCatalogModels(t *testing.T) {
	dir := t.TempDir()
	src := CatalogModels{Manager: models.NewManager(dir, models.Options{Catalog: testCatalog(t)})}
	settings := func(f func(*config.Settings)) config.Settings {
		s := config.Defaults()
		f(&s)
		return s
	}

	// Nothing selected, then selected but not downloaded.
	for _, s := range []config.Settings{
		config.Defaults(),
		settings(func(s *config.Settings) { s.SelectedModel = "multi" }),
		settings(func(s *config.Settings) { s.SelectedModel = "unknown" }),
		settings(func(s *config.Settings) { s.SelectedModel = "vad" }),
	} {
		if _, err := src.Resolve(s); !errors.Is(err, ErrNoModel) {
			t.Errorf("selected %q: err %v, want ErrNoModel", s.SelectedModel, err)
		}
	}

	q8 := put(t, dir, "multi.q8_0.gguf", "multi.q8_0.gguf")
	f16 := put(t, dir, "multi.f16.gguf", "multi.f16.gguf")
	m, err := src.Resolve(settings(func(s *config.Settings) { s.SelectedModel = "multi" }))
	if err != nil {
		t.Fatal(err)
	}
	want := Model{Spec: engine.LoadSpec{ID: "multi", Path: q8, Latency: engine.LatencyBalanced}, Name: "Model multi", Language: "auto"}
	if m != want {
		t.Errorf("model %+v, want %+v (no VAD downloaded yet)", m, want)
	}

	// The settings' quantisation, accelerator, latency and language; the VAD
	// once it is downloaded.
	vad := put(t, dir, "vad.gguf", "vad.gguf")
	m, err = src.Resolve(settings(func(s *config.Settings) {
		s.SelectedModel, s.ModelQuant, s.Accelerator, s.Language = "multi", "F16", config.AcceleratorCPU, "de-DE"
		s.VADOnset, s.VADOffset = 0.6, 0.4
		s.StreamingLatency = config.LatencyLow
	}))
	if err != nil {
		t.Fatal(err)
	}
	want = Model{
		Spec: engine.LoadSpec{
			ID: "multi", Path: f16, CPU: true, VADPath: vad, VADOnset: 0.6, VADOffset: 0.4,
			Latency: engine.LatencyLow,
		},
		Name:     "Model multi",
		Language: "de-DE",
	}
	if m != want {
		t.Errorf("model %+v, want %+v", m, want)
	}
	m, _ = src.Resolve(settings(func(s *config.Settings) { s.SelectedModel, s.VADEnabled = "multi", false }))
	if m.Spec.VADPath != "" {
		t.Errorf("VAD %q with vad_enabled off", m.Spec.VADPath)
	}

	// A model with one language ignores the setting, and an offline one the
	// latency; a quantisation it does not have falls back to its own.
	eng := put(t, dir, "english.gguf", "english.gguf")
	m, err = src.Resolve(settings(func(s *config.Settings) {
		s.SelectedModel, s.ModelQuant, s.Language, s.StreamingLatency = "english", "F16", "de-DE", config.LatencyAccurate
	}))
	if err != nil {
		t.Fatal(err)
	}
	if m.Spec.Path != eng || m.Language != "" || m.Spec.Latency != "" {
		t.Errorf("model %+v", m)
	}

	// A damaged file.
	put(t, dir, "english.gguf", "english.ggux")
	m, err = src.Resolve(settings(func(s *config.Settings) { s.SelectedModel = "english" }))
	if !errors.Is(err, ErrModelCorrupt) || m.Name != "Model english" {
		t.Errorf("model %+v, err %v; want ErrModelCorrupt with the name", m, err)
	}
}
