package pipeline

import (
	"errors"
	"fmt"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/models"
)

// Errors a ModelSource returns; test for them with errors.Is.
var (
	// ErrNoModel: no model is selected, or the selected one is not
	// downloaded, so dictation cannot start.
	ErrNoModel = errors.New("no model downloaded")
	// ErrModelCorrupt: the selected model's file is on disk but fails its
	// checksum.
	ErrModelCorrupt = errors.New("model file is corrupt")
)

// Model is what a session transcribes with.
type Model struct {
	// Spec is what to load.
	Spec engine.LoadSpec
	// Name is the model's display name, for notices.
	Name string
	// Language is the recognition language to ask for: the setting, or ""
	// for a model with one fixed language.
	Language string
}

// ModelSource says which model dictation uses. The coordinator asks at every
// Start, so a model selected or downloaded since is picked up, and Start can
// refuse to record when there is none.
type ModelSource interface {
	// Resolve returns the model the settings select. It fails with an error
	// matching ErrNoModel when there is none ready to load, and
	// ErrModelCorrupt when its file is damaged; Model.Name is set then when
	// the model is known. It never downloads anything.
	Resolve(s config.Settings) (Model, error)
}

// CatalogModels resolves the selected model through the model manager: the
// selected_model setting, in the model_quant quantisation when the model has
// it, with the Silero VAD when vad_enabled and it is downloaded.
type CatalogModels struct {
	Manager *models.Manager
}

// Resolve implements ModelSource.
func (c CatalogModels) Resolve(s config.Settings) (Model, error) {
	if s.SelectedModel == "" {
		return Model{}, fmt.Errorf("%w: no model is selected", ErrNoModel)
	}
	cat := c.Manager.Catalog()
	m, ok := cat.Model(s.SelectedModel)
	if !ok || m.Kind != models.KindASR {
		return Model{}, fmt.Errorf("%w: %q is not a speech model in the catalog", ErrNoModel, s.SelectedModel)
	}
	quant := ""
	if _, ok := m.File(s.ModelQuant); ok {
		quant = s.ModelQuant
	}
	path, err := c.Manager.Path(m.ID, quant)
	switch {
	case errors.Is(err, models.ErrCorrupt):
		return Model{Name: m.Name}, fmt.Errorf("%w: %v", ErrModelCorrupt, err)
	case err != nil:
		return Model{Name: m.Name}, fmt.Errorf("%w: %v", ErrNoModel, err)
	}

	out := Model{
		Spec: engine.LoadSpec{
			ID:   m.ID,
			Path: path,
			CPU:  s.Accelerator == config.AcceleratorCPU,
		},
		Name:     m.Name,
		Language: s.Language,
	}
	// A model with one language ignores the setting; asking for another
	// language could only make it fail.
	if !m.Capabilities.LanguageDetection && len(m.Languages) <= 1 {
		out.Language = ""
	}
	if vad := cat.VAD(); s.VADEnabled && vad != nil {
		if p, err := c.Manager.Path(vad.ID, ""); err == nil {
			out.Spec.VADPath = p
			out.Spec.VADOnset, out.Spec.VADOffset = float32(s.VADOnset), float32(s.VADOffset)
		}
	}
	return out, nil
}
