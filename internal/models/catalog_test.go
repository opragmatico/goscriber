package models

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestDefaultCatalog(t *testing.T) {
	c := Default()
	var ids []string
	for _, m := range c.Models {
		ids = append(ids, m.ID)
	}
	if want := []string{"nemotron-3.5-asr-streaming-0.6b", "parakeet-tdt-0.6b-v2", "silero-vad"}; !slices.Equal(ids, want) {
		t.Fatalf("models %v, want %v", ids, want)
	}
	if c.DefaultModel != "nemotron-3.5-asr-streaming-0.6b" {
		t.Errorf("default model %q", c.DefaultModel)
	}
	if asr := c.ASR(); len(asr) != 2 || c.VAD().ID != "silero-vad" {
		t.Errorf("ASR %d models, VAD %q", len(asr), c.VAD().ID)
	}

	nemo, _ := c.Model("nemotron-3.5-asr-streaming-0.6b")
	if !nemo.Capabilities.Streaming || !nemo.Capabilities.LanguageDetection || !nemo.Capabilities.Timestamps {
		t.Errorf("Nemotron capabilities %+v", nemo.Capabilities)
	}
	if len(nemo.Languages) != 32 || !slices.Contains(nemo.Languages, "en-US") {
		t.Errorf("Nemotron languages %v, want the 32 transcription-ready and broad-coverage locales", nemo.Languages)
	}

	tdt, _ := c.Model("parakeet-tdt-0.6b-v2")
	if tdt.Capabilities.Streaming || tdt.Capabilities.LanguageDetection || !slices.Equal(tdt.Languages, []string{"en"}) {
		t.Errorf("Parakeet TDT v2 must be English-only and batch-only: %+v %v", tdt.Capabilities, tdt.Languages)
	}

	for _, m := range c.ASR() {
		if m.DefaultQuant != "Q8_0" {
			t.Errorf("%s: default quant %q, want Q8_0", m.ID, m.DefaultQuant)
		}
	}
}

// The catalog copies its pins from docs/models/gguf.md; keep the two in step.
func TestCatalogMatchesModelDocs(t *testing.T) {
	doc, err := os.ReadFile("../../docs/models/gguf.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range Default().Models {
		if !strings.Contains(string(doc), "`"+m.Repo+"`") || !strings.Contains(string(doc), m.Revision) {
			t.Errorf("%s: repo %s at %s is not in docs/models/gguf.md", m.ID, m.Repo, m.Revision)
		}
		for _, f := range m.Files {
			size := formatThousands(f.Size)
			if !strings.Contains(string(doc), f.SHA256) || !strings.Contains(string(doc), size) || !strings.Contains(string(doc), "`"+f.Name+"`") {
				t.Errorf("%s: %s (%s bytes, %s) is not in docs/models/gguf.md", m.ID, f.Name, size, f.SHA256)
			}
		}
	}
}

func formatThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	var out []byte
	for i := range len(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out)
}

func TestModelFileAndURL(t *testing.T) {
	tdt, _ := Default().Model("parakeet-tdt-0.6b-v2")
	if f, ok := tdt.File(""); !ok || f.Quant != "Q8_0" {
		t.Errorf("default file %+v", f)
	}
	if f, ok := tdt.File("f16"); !ok || f.Name != "parakeet-tdt-0.6b-v2.f16.gguf" {
		t.Errorf("f16 file %+v", f)
	}
	if _, ok := tdt.File("Q4_K"); ok {
		t.Error("found a Q4_K file")
	}
	f, _ := tdt.File("")
	want := "https://huggingface.co/opragmatico/parakeet-tdt-0.6b-v2-nemo-speech-gguf/resolve/da0de3b9ff0f0b219b71bee25e9ef296f0d9cf8c/parakeet-tdt-0.6b-v2.q8_0.gguf"
	if got := tdt.URL(DefaultEndpoint+"/", f); got != want {
		t.Errorf("URL\n got %s\nwant %s", got, want)
	}
}

func TestParseRejectsBadCatalogs(t *testing.T) {
	base := func() map[string]any {
		var v map[string]any
		if err := json.Unmarshal(catalogJSON, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	model := func(v map[string]any, i int) map[string]any {
		return v["models"].([]any)[i].(map[string]any)
	}
	file := func(v map[string]any, i int) map[string]any {
		return model(v, i)["files"].([]any)[0].(map[string]any)
	}
	for _, tt := range []struct {
		name   string
		mutate func(v map[string]any)
	}{
		{"unknown field", func(v map[string]any) { v["extra"] = 1 }},
		{"duplicate id", func(v map[string]any) { model(v, 1)["id"] = model(v, 0)["id"] }},
		{"unknown kind", func(v map[string]any) { model(v, 0)["kind"] = "tts" }},
		{"short revision", func(v map[string]any) { model(v, 0)["revision"] = "main" }},
		{"bad repo", func(v map[string]any) { model(v, 0)["repo"] = "nvidia" }},
		{"bad sha256", func(v map[string]any) { file(v, 0)["sha256"] = "abc" }},
		{"zero size", func(v map[string]any) { file(v, 0)["size"] = 0 }},
		{"path in filename", func(v map[string]any) { file(v, 0)["filename"] = "../evil.gguf" }},
		{"missing default quant", func(v map[string]any) { model(v, 0)["default_quant"] = "Q4_0" }},
		{"no VAD", func(v map[string]any) { model(v, 2)["kind"] = "asr" }},
		{"default model is the VAD", func(v map[string]any) { v["default_model"] = "silero-vad" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := base()
			tt.mutate(v)
			data, _ := json.Marshal(v)
			if _, err := Parse(data); err == nil {
				t.Error("Parse accepted it")
			}
		})
	}
	if _, err := Parse(catalogJSON); err != nil {
		t.Errorf("the embedded catalog: %v", err)
	}
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 B", 999: "999 B", 1_240_224: "1.2 MB", 742_090_464: "742 MB", 1_278_057_440: "1.3 GB",
	} {
		if got := FormatBytes(n); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestStateString(t *testing.T) {
	for s, want := range map[State]string{NotDownloaded: "not downloaded", Downloading: "downloading", Ready: "ready", Corrupt: "corrupt", 9: "unknown"} {
		if s.String() != want {
			t.Errorf("%d: %q", s, s.String())
		}
	}
}
