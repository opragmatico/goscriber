package models

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"sync"
)

// catalogJSON is the built-in catalog: metadata only, never weights.
//
//go:embed catalog.json
var catalogJSON []byte

// Kind says what a model is for.
type Kind string

// Model kinds.
const (
	// KindASR is a speech recognition model the user can choose.
	KindASR Kind = "asr"
	// KindVAD is the voice activity detector loaded next to the ASR model.
	// It comes with the user's first ASR model download.
	KindVAD Kind = "vad"
)

// Catalog lists the models Goscriber knows how to download and run.
type Catalog struct {
	// DefaultModel is the ASR model offered first, such as in onboarding.
	// It is never selected without the user's say-so.
	DefaultModel string  `json:"default_model"`
	Models       []Model `json:"models"`
}

// Model is one catalog entry: where its files come from and what it can do.
type Model struct {
	ID          string `json:"id"`
	Kind        Kind   `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Repo and Revision pin the Hugging Face repository the files come from.
	Repo       string `json:"repo"`
	Revision   string `json:"revision"`
	License    string `json:"license"`
	LicenseURL string `json:"license_url"`
	// Languages are the locales the model transcribes. Models that detect
	// the language also take "auto" (Capabilities.LanguageDetection).
	Languages    []string     `json:"languages,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
	// DefaultQuant is the quantisation downloaded unless the user picks
	// another one of Files.
	DefaultQuant string `json:"default_quant"`
	Files        []File `json:"files"`
}

// Capabilities are what a model can do, so the app can explain the choice
// before anything is downloaded.
type Capabilities struct {
	// Streaming models show words while the user speaks; the others
	// transcribe once recording stops.
	Streaming bool `json:"streaming"`
	// LanguageDetection: the model takes "auto" and detects the language.
	LanguageDetection bool `json:"language_detection"`
	// Timestamps: the model reports when each word was spoken.
	Timestamps bool `json:"timestamps"`
}

// File is one downloadable quantisation of a model.
type File struct {
	Quant string `json:"quant"`
	// Name is the file's name in the repository and on disk.
	Name   string `json:"filename"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

var (
	defaultOnce sync.Once
	defaultCat  *Catalog
	defaultErr  error
)

// Default returns the built-in catalog. It panics if the embedded file is
// invalid, which the package's tests rule out.
func Default() *Catalog {
	defaultOnce.Do(func() { defaultCat, defaultErr = Parse(catalogJSON) })
	if defaultErr != nil {
		panic(defaultErr)
	}
	return defaultCat
}

// Parse decodes and validates a catalog.
func Parse(data []byte) (*Catalog, error) {
	var c Catalog
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("models: catalog: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("models: catalog: %w", err)
	}
	return &c, nil
}

func (c *Catalog) validate() error {
	var errs []error
	ids := map[string]bool{}
	names := map[string]bool{}
	vads := 0
	for i := range c.Models {
		m := &c.Models[i]
		bad := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("%s: "+format, append([]any{m.ID}, args...)...))
		}
		switch {
		case m.ID == "" || strings.ContainsAny(m.ID, `/\ `):
			errs = append(errs, fmt.Errorf("model %d: bad id %q", i, m.ID))
			continue
		case ids[m.ID]:
			bad("duplicate id")
		}
		ids[m.ID] = true
		switch m.Kind {
		case KindASR:
		case KindVAD:
			vads++
		default:
			bad("unknown kind %q", m.Kind)
		}
		if m.Name == "" || m.License == "" {
			bad("name and license are required")
		}
		if strings.Count(m.Repo, "/") != 1 || strings.HasPrefix(m.Repo, "/") || strings.HasSuffix(m.Repo, "/") {
			bad("repo %q is not owner/name", m.Repo)
		}
		if !isHex(m.Revision, 40) {
			bad("revision %q is not a full commit hash", m.Revision)
		}
		if len(m.Files) == 0 {
			bad("no files")
		}
		if _, ok := m.File(m.DefaultQuant); !ok || m.DefaultQuant == "" {
			bad("default quant %q has no file", m.DefaultQuant)
		}
		quants := map[string]bool{}
		for _, f := range m.Files {
			q := strings.ToUpper(f.Quant)
			if q == "" || quants[q] {
				bad("missing or duplicate quant %q", f.Quant)
			}
			quants[q] = true
			if f.Name == "" || f.Name != path.Base(f.Name) || strings.HasPrefix(f.Name, ".") || names[f.Name] {
				bad("bad or duplicate filename %q", f.Name)
			}
			names[f.Name] = true
			if f.Size <= 0 {
				bad("%s: size must be positive", f.Name)
			}
			if !isHex(f.SHA256, 64) {
				bad("%s: sha256 %q is not 64 hex digits", f.Name, f.SHA256)
			}
		}
	}
	if vads != 1 {
		errs = append(errs, fmt.Errorf("want exactly one VAD model, have %d", vads))
	}
	if m, ok := c.Model(c.DefaultModel); !ok || m.Kind != KindASR {
		errs = append(errs, fmt.Errorf("default model %q is not an ASR model", c.DefaultModel))
	}
	return errors.Join(errs...)
}

func isHex(s string, n int) bool {
	if len(s) != n || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// Model returns the model with the given id.
func (c *Catalog) Model(id string) (*Model, bool) {
	for i := range c.Models {
		if c.Models[i].ID == id {
			return &c.Models[i], true
		}
	}
	return nil, false
}

// ASR returns the speech recognition models, in catalog order.
func (c *Catalog) ASR() []*Model {
	var out []*Model
	for i := range c.Models {
		if c.Models[i].Kind == KindASR {
			out = append(out, &c.Models[i])
		}
	}
	return out
}

// VAD returns the voice activity detector that comes with the ASR models.
func (c *Catalog) VAD() *Model {
	for i := range c.Models {
		if c.Models[i].Kind == KindVAD {
			return &c.Models[i]
		}
	}
	return nil
}

// File returns the file for quant, compared without regard to case; an
// empty quant means DefaultQuant.
func (m *Model) File(quant string) (File, bool) {
	if quant == "" {
		quant = m.DefaultQuant
	}
	for _, f := range m.Files {
		if strings.EqualFold(f.Quant, quant) {
			return f, true
		}
	}
	return File{}, false
}

// Quants lists the model's quantisations, in catalog order.
func (m *Model) Quants() []string {
	qs := make([]string, len(m.Files))
	for i, f := range m.Files {
		qs[i] = f.Quant
	}
	return qs
}

// URL is where f downloads from: endpoint/{repo}/resolve/{revision}/{file}.
// endpoint is the Hugging Face endpoint, such as DefaultEndpoint.
func (m *Model) URL(endpoint string, f File) string {
	return strings.TrimSuffix(endpoint, "/") + "/" + m.Repo + "/resolve/" + m.Revision + "/" + url.PathEscape(f.Name)
}
