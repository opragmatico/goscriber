package app

import (
	"context"
	"errors"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/llm"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/pipeline"
	"github.com/opragmatico/goscriber/internal/ui"
)

// secrets keeps the API keys of LLM providers, by keychain account.
// *keychain.Store implements it.
type secrets interface {
	Get(account string) (string, error)
	Set(account, secret string) error
}

// llmKey returns the API key stored for a provider's base URL.
func (sh *shell) llmKey(baseURL string) (string, error) {
	return sh.svc.keys.Get(llm.KeyAccount(baseURL))
}

// llmStage is the LLM text stage, for dictations and files alike: it uses
// the settings each session started with, and tells the user when the
// model could not clean a transcript.
func (sh *shell) llmStage() *llm.Stage {
	return &llm.Stage{
		Client: sh.llm,
		Key:    sh.llmKey,
		Memory: sh.memory,
		Failed: func(err error) {
			if !errors.Is(err, context.Canceled) {
				sh.notices.Publish(notice.LLMFailed(err))
			}
		},
		Log: sh.rt.Log.Logger,
	}
}

// llmActions back the settings window's Post-processing page.
func (sh *shell) llmActions() ui.LLMActions {
	return ui.LLMActions{
		Models: func(ctx context.Context, baseURL string) ([]string, error) {
			key, err := sh.llmKey(baseURL)
			if err != nil {
				sh.rt.Log.Warn("the API key could not be read from the keychain; sending none", "err", err)
			}
			return sh.llm.Models(ctx, llm.Endpoint{BaseURL: baseURL, APIKey: key})
		},
		HasKey: func(baseURL string) (bool, error) {
			key, err := sh.llmKey(baseURL)
			return key != "", err
		},
		SetKey: func(baseURL, key string) error { return sh.svc.keys.Set(llm.KeyAccount(baseURL), key) },
		Try:    sh.tryLLM,
		Memory: sh.memory,
	}
}

// tryLLM has the model the settings name clean the sample transcript, for
// the Try button: whether post-processing is on or not.
func (sh *shell) tryLLM(ctx context.Context, s config.Settings) (string, error) {
	s.LLMEnabled = true
	stage := sh.llmStage()
	stage.Failed = nil
	return stage.Process(ctx, llm.SampleTranscript, pipeline.SessionInfo{Settings: s})
}
