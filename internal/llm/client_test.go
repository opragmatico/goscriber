package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
)

// fakeServer is an OpenAI-compatible provider. Its answer function sees
// each chat request, decoded, and returns the status and the body to
// send back.
type fakeServer struct {
	t      *testing.T
	srv    *httptest.Server
	answer func(req map[string]any) (int, string)

	mu       sync.Mutex
	requests []map[string]any
	auth     []string
}

func newFakeServer(t *testing.T, answer func(req map[string]any) (int, string)) *fakeServer {
	t.Helper()
	f := &fakeServer{t: t, answer: answer}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gemma4:latest"},{"id":"gemma4:26b"},{"id":"gemma4:26b"}]}`))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.t.Errorf("request body: %v", err)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			f.t.Errorf("Content-Type = %q", ct)
		}
		f.mu.Lock()
		f.requests = append(f.requests, req)
		f.mu.Unlock()
		status, body := f.answer(req)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeServer) endpoint() Endpoint { return Endpoint{BaseURL: f.srv.URL + "/v1"} }

func (f *fakeServer) sent() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// reply is a chat completion answering content, stopping for reason.
func reply(content, reason string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": reason,
		}},
	})
	return string(b)
}

// userText is the prompt of a chat request.
func userText(req map[string]any) string {
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 1 {
		return ""
	}
	m, _ := msgs[0].(map[string]any)
	s, _ := m["content"].(string)
	return s
}

func TestCompleteRequest(t *testing.T) {
	f := newFakeServer(t, func(map[string]any) (int, string) { return 200, reply("Cleaned.", "stop") })
	var c Client
	ep := f.endpoint()
	ep.APIKey = "sk-secret"
	got, err := c.Complete(context.Background(), ep, "gemma4:26b", "Clean this", 1200)
	if err != nil || got != "Cleaned." {
		t.Fatalf("Complete = %q, %v", got, err)
	}
	req := f.sent()[0]
	want := map[string]any{
		"model":            "gemma4:26b",
		"messages":         []any{map[string]any{"role": "user", "content": "Clean this"}},
		"stream":           false,
		"reasoning_effort": "none",
		"max_tokens":       float64(1200),
	}
	for k, v := range want {
		if b1, _ := json.Marshal(req[k]); string(b1) != mustJSON(v) {
			t.Errorf("%s = %s, want %s", k, b1, mustJSON(v))
		}
	}
	if _, ok := req["max_completion_tokens"]; ok {
		t.Error("max_completion_tokens sent to a provider other than OpenAI")
	}
	if f.auth[0] != "Bearer sk-secret" {
		t.Errorf("Authorization = %q", f.auth[0])
	}

	// Without a key, no Authorization header.
	if _, err := c.Complete(context.Background(), f.endpoint(), "m", "p", 10); err != nil {
		t.Fatal(err)
	}
	if f.auth[1] != "" {
		t.Errorf("Authorization without a key = %q", f.auth[1])
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// OpenAI's API gets max_completion_tokens, which its reasoning models
// require, instead of max_tokens.
func TestCompleteOpenAICap(t *testing.T) {
	f := newFakeServer(t, func(map[string]any) (int, string) { return 200, reply("ok", "stop") })
	target, _ := url.Parse(f.srv.URL)
	c := Client{HTTP: &http.Client{Transport: redirect{target}}}
	if _, err := c.Complete(context.Background(), Endpoint{BaseURL: "https://api.openai.com/v1"}, "o4-mini", "p", 1200); err != nil {
		t.Fatal(err)
	}
	req := f.sent()[0]
	if req["max_completion_tokens"] != float64(1200) || req["max_tokens"] != nil {
		t.Errorf("max_completion_tokens %v, max_tokens %v; want 1200 and none", req["max_completion_tokens"], req["max_tokens"])
	}
}

// OpenAI's default model is asked to think at "medium", with no step-down,
// and room for the thinking: a cap of thinkingMaxTokens and
// ThinkingTimeout. A server that rejects the effort fails the request.
func TestCompleteModelEffort(t *testing.T) {
	var status int
	f := newFakeServer(t, func(map[string]any) (int, string) {
		if status != 0 {
			return status, `{"error":{"message":"unsupported reasoning_effort"}}`
		}
		return 200, reply("ok", "stop")
	})
	target, _ := url.Parse(f.srv.URL)
	c := Client{HTTP: &http.Client{Transport: redirect{target}}}
	openAI := Endpoint{BaseURL: "https://api.openai.com/v1"}
	if got, err := c.Complete(context.Background(), openAI, OpenAIModel, "p", 1200); err != nil || got != "ok" {
		t.Fatalf("Complete = %q, %v", got, err)
	}
	req := f.sent()[0]
	if req["reasoning_effort"] != "medium" || req["max_completion_tokens"] != float64(thinkingMaxTokens) {
		t.Errorf("reasoning_effort %v, max_completion_tokens %v; want medium and %d", req["reasoning_effort"], req["max_completion_tokens"], thinkingMaxTokens)
	}

	status = 400
	if _, err := c.Complete(context.Background(), openAI, OpenAIModel, "p", 1200); err == nil {
		t.Error("a rejected effort reported success")
	}
	if n := len(f.sent()); n != 2 {
		t.Errorf("%d requests, want one per completion: no step-down", n)
	}

	if got := (&Client{}).thinkingTimeout(); got != ThinkingTimeout {
		t.Errorf("thinking timeout %v, want %v", got, ThinkingTimeout)
	}
}

// Gemini's models think however little reasoning_effort asks for, so its
// requests leave room for the thinking; its model list loses the
// "models/" prefix, which its API does not need.
func TestGemini(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1beta/openai/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"models/gemini-4-hydrogen"},{"id":"models/gemini-3.8-flash"}]}`))
		case "/v1beta/openai/chat/completions":
			_ = json.NewDecoder(r.Body).Decode(&sent)
			_, _ = w.Write([]byte(reply("ok", "stop")))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	c := Client{HTTP: &http.Client{Transport: redirect{target}}}
	ep := Endpoint{BaseURL: config.LLMGemini.BaseURL(), APIKey: "k"}

	if _, err := c.Complete(context.Background(), ep, GeminiModel, "p", 1200); err != nil {
		t.Fatal(err)
	}
	if sent["max_tokens"] != float64(thinkingMaxTokens) || sent["reasoning_effort"] != "none" {
		t.Errorf("max_tokens %v, reasoning_effort %v; want %d and none", sent["max_tokens"], sent["reasoning_effort"], thinkingMaxTokens)
	}
	models, err := c.Models(context.Background(), ep)
	if err != nil || !slices.Equal(models, []string{"gemini-3.8-flash", "gemini-4-hydrogen"}) {
		t.Errorf("Models = %v, %v", models, err)
	}
}

// redirect sends every request to target, whatever its URL says.
type redirect struct{ target *url.URL }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.target.Scheme, r.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

// A server that rejects reasoning_effort gets the request again with the
// next least: "minimal" after "none", then "low", then none at all. The
// first it takes is
// where that model starts from then on; other models start from "none".
func TestCompleteReasoningFallback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		takes  func(effort string, ok bool) bool
		want   []string // reasoning_effort per request, "-" for none
	}{
		// Like Ollama before reasoning_effort, or llama.cpp.
		{"no field 400", 400, func(_ string, ok bool) bool { return !ok }, []string{"none", "minimal", "low", "-", "-", "none"}},
		{"no field 422", 422, func(_ string, ok bool) bool { return !ok }, []string{"none", "minimal", "low", "-", "-", "none"}},
		// Like Gemini 3, whose thinking cannot be turned off.
		{"no none", 400, func(e string, _ bool) bool { return e != "none" }, []string{"none", "minimal", "minimal", "none"}},
		// Like GPT-6.1 Sol, which thinks at "medium" without the field.
		{
			"low at the least", 400, func(e string, ok bool) bool { return ok && e != "none" && e != "minimal" },
			[]string{"none", "minimal", "low", "low", "none"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t, func(req map[string]any) (int, string) {
				effort, ok := req["reasoning_effort"].(string)
				if req["model"] == "picky" && !tc.takes(effort, ok) {
					return tc.status, `{"error":{"message":"unsupported reasoning_effort"}}`
				}
				return 200, reply("fine", "stop")
			})
			var c Client
			for range 2 {
				if got, err := c.Complete(context.Background(), f.endpoint(), "picky", "p", 10); err != nil || got != "fine" {
					t.Fatalf("Complete = %q, %v", got, err)
				}
			}
			if _, err := c.Complete(context.Background(), f.endpoint(), "other", "p", 10); err != nil {
				t.Fatal(err)
			}
			var efforts []string
			for _, r := range f.sent() {
				e, ok := r["reasoning_effort"].(string)
				if !ok {
					e = "-"
				}
				efforts = append(efforts, e)
			}
			if !slices.Equal(efforts, tc.want) {
				t.Errorf("reasoning_effort sent: %v, want %v", efforts, tc.want)
			}
		})
	}
}

// A 400 that the retry gets too was not about reasoning_effort: it is
// returned, and the field is not dropped for later requests.
func TestCompleteBadRequestNotRemembered(t *testing.T) {
	f := newFakeServer(t, func(map[string]any) (int, string) {
		return 400, `{"error":{"message":"model 'nope' not found"}}`
	})
	var c Client
	_, err := c.Complete(context.Background(), f.endpoint(), "nope", "p", 10)
	se, ok := errors.AsType[*StatusError](err)
	if !ok || se.Status != 400 || se.Message != "model 'nope' not found" {
		t.Fatalf("err = %v, want the 400 with its message", err)
	}
	if !strings.Contains(err.Error(), "model 'nope' not found") {
		t.Errorf("error text %q lacks the server's message", err)
	}
	if _, err := c.Complete(context.Background(), f.endpoint(), "nope", "p", 10); err == nil {
		t.Fatal("no error")
	}
	sent := f.sent()
	if len(sent) != 2*len(efforts) {
		t.Fatalf("%d requests, want %d per completion", len(sent), len(efforts))
	}
	if e := sent[len(efforts)]["reasoning_effort"]; e != "none" {
		t.Errorf("the second completion started from reasoning_effort %v, want none: retries that failed the same way were remembered", e)
	}
}

func TestCompleteFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   func(error) bool
	}{
		{"stopped at the cap", 200, reply("and again and again and", "length"), func(err error) bool { return errors.Is(err, ErrTruncated) }},
		{"no choice", 200, `{"choices":[]}`, func(err error) bool { return errors.Is(err, ErrNoChoice) }},
		{"not JSON", 200, `<html>`, func(err error) bool { return err != nil && strings.Contains(err.Error(), "JSON") }},
		{"unauthorized", 401, `{"error":{"message":"Incorrect API key provided"}}`, func(err error) bool {
			se, ok := errors.AsType[*StatusError](err)
			return ok && se.Status == 401 && se.Message == "Incorrect API key provided"
		}},
		{"plain error", 500, `{"error":"model crashed"}`, func(err error) bool {
			se, ok := errors.AsType[*StatusError](err)
			return ok && se.Message == "model crashed"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t, func(map[string]any) (int, string) { return tc.status, tc.body })
			var c Client
			got, err := c.Complete(context.Background(), f.endpoint(), "m", "p", 10)
			if !tc.want(err) {
				t.Errorf("Complete = %q, %v", got, err)
			}
		})
	}
}

func TestCompleteTimeout(t *testing.T) {
	release := make(chan struct{})
	f := newFakeServer(t, func(map[string]any) (int, string) {
		<-release
		return 200, reply("late", "stop")
	})
	defer close(release)
	c := Client{Timeout: 50 * time.Millisecond}
	start := time.Now()
	_, err := c.Complete(context.Background(), f.endpoint(), "m", "p", 10)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "no answer within") {
		t.Errorf("err = %v, want a timeout", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %s", d)
	}
}

func TestCompleteBadBaseURL(t *testing.T) {
	var c Client
	if _, err := c.Complete(context.Background(), Endpoint{BaseURL: "localhost:11434"}, "m", "p", 10); err == nil {
		t.Error("no error for a base URL without a scheme")
	}
}

func TestModels(t *testing.T) {
	f := newFakeServer(t, nil)
	var c Client
	got, err := c.Models(context.Background(), f.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gemma4:26b", "gemma4:latest"}; !slices.Equal(got, want) {
		t.Errorf("Models = %v, want %v", got, want)
	}
	// A base URL with a trailing slash works the same.
	if _, err := c.Models(context.Background(), Endpoint{BaseURL: f.srv.URL + "/v1/"}); err != nil {
		t.Error(err)
	}
}

func TestIsLocal(t *testing.T) {
	for url, want := range map[string]bool{
		"http://localhost:11434/v1":    true,
		"http://127.0.0.1:1234/v1":     true,
		"http://[::1]:9931/v1":         true,
		"http://ollama.localhost/v1":   true,
		"https://api.openai.com/v1":    false,
		"http://192.168.1.10:11434/v1": false,
		"nonsense":                     false,
	} {
		if got := IsLocal(url); got != want {
			t.Errorf("IsLocal(%q) = %v", url, got)
		}
	}
}

func TestModelFor(t *testing.T) {
	const gb = 1 << 30
	ollama, groq, claude := ProviderByID(config.LLMOllama), ProviderByID(config.LLMGroq), ProviderByID(config.LLMClaude)
	for _, tc := range []struct {
		p          Provider
		url, model string
		memory     uint64
		want       string
		err        bool
	}{
		{ollama, "http://localhost:11434/v1", "", 36 * gb, LargeModel, false},
		{ollama, "http://localhost:11434/v1", "", 32 * gb, LargeModel, false},
		{ollama, "http://localhost:11434/v1", "", 31 * gb, LargeModel, false}, // a 32 GB Linux machine
		{ollama, "http://localhost:11434/v1", "", 16 * gb, SmallModel, false},
		{ollama, "http://localhost:11434/v1", "", 0, SmallModel, false},
		{ollama, "http://localhost:11434/v1", "qwen3.5:4b", 36 * gb, "qwen3.5:4b", false},
		{ollama, "http://gpu-box.lan:11434/v1", "", 36 * gb, "", true},
		{groq, groq.BaseURL, "", 36 * gb, "", true},
		{groq, groq.BaseURL, "llama-3.1-8b-instant", 0, "llama-3.1-8b-instant", false},
		{claude, claude.BaseURL, "", 0, ClaudeModel, false},
		{ProviderByID(config.LLMGemini), config.LLMGemini.BaseURL(), "", 0, GeminiModel, false},
		{ProviderByID(config.LLMOpenAI), config.LLMOpenAI.BaseURL(), "", 0, OpenAIModel, false},
		{ProviderByID(config.LLMOpenRouter), config.LLMOpenRouter.BaseURL(), "", 0, "", true},
		{claude, claude.BaseURL, "claude-haiku-5-5", 0, "claude-haiku-5-5", false},
	} {
		got, err := ModelFor(tc.p, tc.url, tc.model, tc.memory)
		if got != tc.want || (err != nil) != tc.err {
			t.Errorf("ModelFor(%s, %q, %q, %d GB) = %q, %v", tc.p.Name, tc.url, tc.model, tc.memory/gb, got, err)
		}
	}
}

func TestFormatMemory(t *testing.T) {
	if got := FormatMemory(36 << 30); got != "36 GB" {
		t.Errorf("FormatMemory = %q", got)
	}
}
