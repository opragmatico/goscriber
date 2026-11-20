package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/llm"
)

// fakeLLM backs the Post-processing page: a keychain in memory and a
// provider serving two models.
type fakeLLM struct {
	mu     sync.Mutex
	keys   map[string]string
	listed []string // base URLs whose models were listed
	tried  []config.Settings
}

func (f *fakeLLM) actions() LLMActions {
	return LLMActions{
		Models: func(_ context.Context, baseURL string) ([]string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.listed = append(f.listed, baseURL)
			if strings.Contains(baseURL, "down") {
				return nil, errors.New("connection refused")
			}
			return []string{"gemma4:26b", "gemma4:latest"}, nil
		},
		HasKey: func(baseURL string) (bool, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.keys[baseURL] != "", nil
		},
		SetKey: func(baseURL, key string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if key == "" {
				delete(f.keys, baseURL)
			} else {
				f.keys[baseURL] = key
			}
			return nil
		},
		Try: func(_ context.Context, s config.Settings) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.tried = append(f.tried, s)
			return "The meeting is on Tuesday at 3:30.", nil
		},
		Memory: 36 << 30,
	}
}

func newLLMWindow(t *testing.T) (*testWindow, *fakeLLM) {
	t.Helper()
	f := &fakeLLM{keys: map[string]string{}}
	tw := newTestWindow(t, func(o *SettingsOptions) { o.LLM = f.actions() })
	tw.show(PagePostProcessing)
	return tw, f
}

// boundAs is the widget of type T bound to a settings field.
func boundAs[T fyne.CanvasObject](t *testing.T, tw *testWindow, key string) T {
	t.Helper()
	for _, b := range tw.bindings {
		if w, ok := b.widget.(T); ok && b.key == key {
			return w
		}
	}
	var zero T
	t.Fatalf("no %T is bound to %s", zero, key)
	return zero
}

func TestPostProcessingProvider(t *testing.T) {
	tw, f := newLLMWindow(t)
	provider := boundAs[*widget.Select](t, tw, "llm_base_url")
	url := boundAs[*widget.Entry](t, tw, "llm_base_url")
	if provider.Selected != "Ollama" || url.Text != config.DefaultLLMBaseURL {
		t.Fatalf("provider %q, base URL %q; want Ollama's", provider.Selected, url.Text)
	}

	provider.SetSelected("OpenAI")
	tw.q.drain()
	if got := tw.store.Get().LLMBaseURL; got != "https://api.openai.com/v1" || url.Text != got {
		t.Errorf("after picking OpenAI: stored %q, shown %q", got, url.Text)
	}

	// A base URL of no known provider is Other; one that is no URL is not
	// saved.
	url.SetText("http://gpu-box.lan:8000/v1")
	if got := tw.store.Get().LLMBaseURL; got != "http://gpu-box.lan:8000/v1" || provider.Selected != LabelOtherProvider {
		t.Errorf("typed base URL: stored %q, provider %q", got, provider.Selected)
	}
	url.SetText("gpu-box.lan")
	if got := tw.reopen(t).LLMBaseURL; got != "http://gpu-box.lan:8000/v1" {
		t.Errorf("a base URL without a scheme was saved: %q", got)
	}
	tw.q.drain()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.listed) == 0 || f.listed[0] != config.DefaultLLMBaseURL {
		t.Errorf("models listed for %v, want Ollama's first", f.listed)
	}
}

func TestPostProcessingModel(t *testing.T) {
	tw, _ := newLLMWindow(t)
	model := boundAs[*widget.SelectEntry](t, tw, "llm_model")
	if model.PlaceHolder != llm.LargeModel {
		t.Errorf("placeholder %q, want the model suggested for 36 GB", model.PlaceHolder)
	}
	text := tw.pageText(PagePostProcessing)
	for _, want := range []string{
		"Left empty, gemma4:26b, suggested for this computer's 36 GB of memory.",
		"ollama pull gemma4:26b",
		"The provider serves: gemma4:26b, gemma4:latest.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the page lacks %q:\n%s", want, text)
		}
	}

	model.SetText("gemma4:latest")
	if got := tw.reopen(t).LLMModel; got != "gemma4:latest" {
		t.Errorf("llm_model = %q", got)
	}
	if strings.Contains(tw.pageText(PagePostProcessing), "Left empty") {
		t.Error("the suggestion is still explained with a model chosen")
	}

	// A provider that cannot be reached says so; a hosted one wants a model.
	boundAs[*widget.Entry](t, tw, "llm_base_url").SetText("http://down.example/v1")
	model.SetText("")
	tw.q.until(t, "the failed listing", func() bool {
		return strings.Contains(tw.pageText(PagePostProcessing), "Could not list the models of http://down.example/v1: connection refused")
	})
	if model.PlaceHolder != "Choose a model" {
		t.Errorf("placeholder for a hosted provider %q", model.PlaceHolder)
	}
}

func TestPostProcessingPrompt(t *testing.T) {
	tw, _ := newLLMWindow(t)
	prompt := boundAs[*widget.Entry](t, tw, "llm_prompt")
	if prompt.Text != llm.DefaultPrompt || !strings.Contains(tw.pageText(PagePostProcessing), llmPromptBuiltIn) {
		t.Fatalf("the built-in prompt is not shown: %q", prompt.Text)
	}
	builtIn := tw.buttons(PagePostProcessing)[LabelBuiltInPrompt]
	if !builtIn.Disabled() {
		t.Error("Use the built-in prompt enabled while it is in use")
	}

	prompt.SetText("Fix this: ${output}")
	if got := tw.reopen(t).LLMPrompt; got != "Fix this: ${output}" {
		t.Errorf("llm_prompt = %q", got)
	}
	if builtIn.Disabled() || !strings.Contains(tw.pageText(PagePostProcessing), llmPromptCustom) {
		t.Error("an edited prompt is not shown as the user's own")
	}

	test.Tap(builtIn)
	if got := tw.reopen(t).LLMPrompt; got != "" || prompt.Text != llm.DefaultPrompt {
		t.Errorf("after Use the built-in prompt: stored %q, shown %.30q", got, prompt.Text)
	}

	// Clearing the field is the built-in prompt too, but the field stays
	// empty for the user's own until they ask for the built-in one.
	prompt.SetText("Mine: ${output}")
	prompt.SetText("")
	if got := tw.reopen(t).LLMPrompt; got != "" || prompt.Text != "" || builtIn.Disabled() {
		t.Errorf("a cleared prompt: saved %q, shown %q, button disabled %v", got, prompt.Text, builtIn.Disabled())
	}
	test.Tap(builtIn)
	if prompt.Text != llm.DefaultPrompt || !builtIn.Disabled() {
		t.Errorf("after Use the built-in prompt on an empty field: shown %.30q", prompt.Text)
	}
}

func TestPostProcessingKey(t *testing.T) {
	tw, f := newLLMWindow(t)
	if !strings.Contains(tw.pageText(PagePostProcessing), llmNoKeyHint) {
		t.Error("the page does not say no key is stored")
	}
	if !tw.buttons(PagePostProcessing)[LabelRemoveKey].Disabled() {
		t.Error("Remove is enabled with no key stored")
	}
	tw.llm.key.SetText("sk-secret")
	test.Tap(tw.buttons(PagePostProcessing)[LabelSaveKey])
	tw.q.until(t, "the key stored", func() bool { return strings.Contains(tw.pageText(PagePostProcessing), llmKeyStoredHint) })
	f.mu.Lock()
	if f.keys[config.DefaultLLMBaseURL] != "sk-secret" {
		t.Errorf("keychain holds %v", f.keys)
	}
	f.mu.Unlock()
	if tw.llm.key.Text != "" || strings.Contains(tw.store.Path(), "sk-secret") {
		t.Error("the key stays in the field")
	}
	if s := tw.reopen(t); strings.Contains(s.LLMBaseURL+s.LLMModel+s.LLMPrompt, "sk-secret") {
		t.Error("the key went to the settings")
	}

	test.Tap(tw.buttons(PagePostProcessing)[LabelRemoveKey])
	tw.q.until(t, "the key removed", func() bool { return strings.Contains(tw.pageText(PagePostProcessing), llmNoKeyHint) })
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.keys) != 0 {
		t.Errorf("keychain still holds %v", f.keys)
	}
}

func TestPostProcessingTry(t *testing.T) {
	tw, f := newLLMWindow(t)
	test.Tap(tw.buttons(PagePostProcessing)[LabelTryLLM])
	tw.q.until(t, "the answer", func() bool {
		return strings.Contains(tw.pageText(PagePostProcessing), "“The meeting is on Tuesday at 3:30.”")
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tried) != 1 || f.tried[0].LLMBaseURL != config.DefaultLLMBaseURL {
		t.Errorf("tried with %+v", f.tried)
	}
}

// Without LLM actions the page still edits every setting, with no key,
// listing or Try.
func TestPostProcessingWithoutActions(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.show(PagePostProcessing)
	b := tw.buttons(PagePostProcessing)
	for _, label := range []string{LabelSaveKey, LabelRefreshModels, LabelTryLLM} {
		if b[label] != nil {
			t.Errorf("%s shown without its action", label)
		}
	}
	test.Tap(boundAs[*widget.Check](t, tw, "llm_enabled"))
	if !tw.reopen(t).LLMEnabled {
		t.Error("llm_enabled was not saved")
	}
}
