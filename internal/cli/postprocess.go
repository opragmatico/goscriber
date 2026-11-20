package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/keychain"
	"github.com/opragmatico/goscriber/internal/llm"
)

// postProcessor is transcribe --llm: the language model each transcript
// goes to after the speech model.
type postProcessor struct {
	client *llm.Client
	job    llm.Job
}

// newPostProcessor resolves --llm, --llm-url and --llm-model against the
// settings' post-processing, and reads the provider's API key from the
// keychain. --llm-url picks the provider at that address, or a custom
// OpenAI-compatible server.
func newPostProcessor(env Env, s config.Settings, fl transcribeFlags) (*postProcessor, error) {
	p := llm.ProviderOf(s)
	baseURL := p.URL(s)
	if fl.llmURL != "" {
		baseURL = fl.llmURL
		p = llm.ProviderByID(config.LLMProviderFor(baseURL))
	}
	if _, err := llm.ParseBaseURL(baseURL); err != nil {
		return nil, fmt.Errorf("--llm-url: %w", err)
	}
	model, err := llm.ModelFor(p, baseURL, firstNonEmpty(fl.llmModel, s.LLMModel), llm.TotalMemory())
	if err != nil {
		return nil, fmt.Errorf("%w; choose one with --llm-model", err)
	}
	var key string
	if p.Key != llm.NoKey {
		getKey := env.LLMKey
		if getKey == nil {
			store := keychain.New(llm.KeychainService)
			getKey = func(baseURL string) (string, error) { return store.Get(llm.KeyAccount(baseURL)) }
		}
		if key, err = getKey(baseURL); err != nil {
			fmt.Fprintf(env.Stderr, "goscriber: sending the language model no API key: %v\n", err)
		}
	}
	return &postProcessor{
		client: &llm.Client{},
		job: llm.Job{
			Endpoint: p.Endpoint(baseURL, key),
			Model:    model,
			Prompt:   s.LLMPrompt,
			Vars:     llm.Vars{Words: append(slices.Clone(s.CustomWords), fl.words...)},
		},
	}, nil
}

// check asks the provider for its models before any audio is decoded, so a
// server that is down, or lacks the model, fails at once. A provider that
// cannot list its models is not held to it.
func (p *postProcessor) check(ctx context.Context) error {
	models, err := p.client.Models(ctx, p.job.Endpoint)
	if se, ok := errors.AsType[*llm.StatusError](err); ok && se.Status == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("the language model at %s cannot be reached: %w", p.job.BaseURL, err)
	}
	if len(models) == 0 || slices.Contains(models, p.job.Model) {
		return nil
	}
	msg := fmt.Sprintf("%s does not serve %s", p.job.BaseURL, p.job.Model)
	if llm.IsLocal(p.job.BaseURL) {
		msg += fmt.Sprintf(" (with Ollama: ollama pull %s)", p.job.Model)
	}
	return fmt.Errorf("%s; it serves %s, which --llm-model can pick", msg, strings.Join(models, ", "))
}

// clean has the model clean text, in the language lang ("" or "auto" when
// unknown). A part it failed on is left as it was, and the error says how
// many.
func (p *postProcessor) clean(ctx context.Context, text, lang string) (string, time.Duration, error) {
	start := time.Now()
	job := p.job
	job.Vars.Language = llm.LanguageName(lang)
	res := p.client.Clean(ctx, job, text)
	took := time.Since(start)
	if err := ctx.Err(); err != nil {
		return "", took, err
	}
	if res.Failed > 0 {
		return res.Text, took, fmt.Errorf("the language model failed on %d of %d parts, printed as transcribed: %w", res.Failed, res.Chunks, res.Err)
	}
	return res.Text, took, nil
}
