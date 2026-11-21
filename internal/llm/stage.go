package llm

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/models"
	"github.com/opragmatico/goscriber/internal/pipeline"
)

// Stage is the LLM text stage of dictation and file transcription: with
// llm_enabled, or for a dictation started with the post-processing
// shortcut (SessionInfo.PostProcess), it has the model the settings name
// clean the transcript after the cleanup stage, and passes the rest on
// unchanged.
type Stage struct {
	// Client sends the requests. Required.
	Client *Client
	// Key returns the API key stored for a base URL; nil, or "", sends
	// none.
	Key func(baseURL string) (string, error)
	// Memory is the computer's memory, which picks the suggested model.
	Memory uint64
	// Failed, if set, hears why a transcript was left, wholly or in part,
	// as cleanup left it, so the user can be told.
	Failed func(err error)
	// Log receives a line per transcript; nil means slog's default logger.
	Log *slog.Logger
}

var _ pipeline.TextStage = (*Stage)(nil)

// Process implements pipeline.TextStage. A transcript whose every chunk
// failed fails, so the coordinator passes it on as it was; one that failed
// in part comes back with the failed chunks as they were.
func (s *Stage) Process(ctx context.Context, text string, info pipeline.SessionInfo) (string, error) {
	set := info.Settings
	if !info.PostProcesses() || strings.TrimSpace(text) == "" {
		return text, nil
	}
	job, err := s.Job(set)
	if err != nil {
		s.failed(err)
		return "", err
	}
	job.Vars.Language = LanguageName(info.TextLanguage)
	start := time.Now()
	res := s.Client.Clean(ctx, job, text)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	s.log().Info("transcript post-processed", "session", info.Session, "model", job.Model,
		"chunks", res.Chunks, "failed", res.Failed, "took", time.Since(start).Round(time.Millisecond))
	if res.Failed == 0 {
		return res.Text, nil
	}
	err = res.Err
	if res.Chunks > 1 {
		err = fmt.Errorf("%d of %d parts failed: %w", res.Failed, res.Chunks, res.Err)
	}
	s.failed(err)
	if res.Failed == res.Chunks {
		return "", err
	}
	s.log().Warn("post-processing failed on part of a transcript; keeping those parts as they were", "err", err)
	return res.Text, nil
}

// Job is what the settings ask a transcript to be cleaned with: their
// provider at its base URL with the key stored for it, model (or the
// provider's default), prompt and custom words. The language is the
// transcript's, which the settings do not know.
func (s *Stage) Job(set config.Settings) (Job, error) {
	p := ProviderOf(set)
	baseURL := p.URL(set)
	model, err := ModelFor(p, baseURL, set.LLMModel, s.Memory)
	if err != nil {
		return Job{}, err
	}
	var key string
	if s.Key != nil && p.Key != NoKey {
		if key, err = s.Key(baseURL); err != nil {
			s.log().Warn("the API key could not be read from the keychain; sending none", "err", err)
		}
	}
	job := Job{
		Endpoint: p.Endpoint(baseURL, key),
		Model:    model,
		Prompt:   set.LLMPrompt,
		Vars:     Vars{Words: set.CustomWords},
	}
	job.ChunkWords, job.ContextWords = p.Chunking()
	return job, nil
}

// LanguageName is a transcript language code, such as "pt-BR", by name,
// for the prompt's LanguagePlaceholder; "" for an unknown language.
func LanguageName(code string) string {
	if code == "" || code == models.LanguageAuto {
		return ""
	}
	return models.LanguageName(code)
}

func (s *Stage) failed(err error) {
	if s.Failed != nil {
		s.Failed(err)
	}
}

func (s *Stage) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}
