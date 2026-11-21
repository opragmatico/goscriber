package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

// fakeClaude is Anthropic's Messages API. Its answer function sees each
// message request, decoded, with its headers.
type fakeClaude struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []map[string]any
	headers  []http.Header
}

func newFakeClaude(t *testing.T, answer func(req map[string]any, h http.Header) (int, string)) *fakeClaude {
	t.Helper()
	f := &fakeClaude{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The SDK sends the key as a bearer token; x-api-key is the older form.
		if r.Header.Get("Authorization") != "Bearer sk-ant-test" && r.Header.Get("X-Api-Key") != "sk-ant-test" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/models":
			_, _ = io.WriteString(w, `{"data":[`+
				`{"id":"claude-sonnet-5-5","type":"model","display_name":"Claude Sonnet 5.5","created_at":"2026-01-01T00:00:00Z"},`+
				`{"id":"claude-opus-5-5","type":"model","display_name":"Claude Opus 5.5","created_at":"2026-01-01T00:00:00Z"}],`+
				`"has_more":false,"first_id":"claude-sonnet-5-5","last_id":"claude-opus-5-5"}`)
		case "POST /v1/messages":
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("request body: %v", err)
			}
			f.mu.Lock()
			f.requests = append(f.requests, req)
			f.headers = append(f.headers, r.Header.Clone())
			f.mu.Unlock()
			status, body := answer(req, r.Header)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeClaude) endpoint(key string) Endpoint {
	return Endpoint{BaseURL: f.srv.URL, APIKey: key, API: Messages}
}

func (f *fakeClaude) sent() ([]map[string]any, []http.Header) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests), slices.Clone(f.headers)
}

// claudeReply is a message whose text is text, stopped for stop.
func claudeReply(text, stop string) string {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "", "signature": "sig"},
			map[string]any{"type": "text", "text": text},
		},
		"stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	return string(b)
}

// Claude gets one user message with the least thinking (effort "low"),
// room for that thinking under max_tokens, and server-side fallbacks from
// a model that takes them; the answer is its text.
func TestClaudeComplete(t *testing.T) {
	f := newFakeClaude(t, func(map[string]any, http.Header) (int, string) {
		return 200, claudeReply("Cleaned.", "end_turn")
	})
	var c Client
	got, err := c.Complete(context.Background(), f.endpoint("sk-ant-test"), "claude-opus-5-5", "Clean: hi", 1200)
	if err != nil || got != "Cleaned." {
		t.Fatalf("Complete = %q, %v", got, err)
	}
	reqs, headers := f.sent()
	req := reqs[0]
	if req["model"] != "claude-opus-5-5" || req["max_tokens"] != float64(1200+thinkingMaxTokens) {
		t.Errorf("model %v, max_tokens %v", req["model"], req["max_tokens"])
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Errorf("messages = %v, want one user message", req["messages"])
	}
	if oc, _ := req["output_config"].(map[string]any); oc["effort"] != "low" {
		t.Errorf("output_config = %v, want effort low", req["output_config"])
	}
	if req["fallbacks"] != "default" || headers[0].Get("Anthropic-Beta") != "server-side-fallback-2026-07-01" {
		t.Errorf("fallbacks %v with beta %q", req["fallbacks"], headers[0].Get("Anthropic-Beta"))
	}
	if th, _ := req["thinking"].(map[string]any); th["type"] != "between_tools" {
		t.Errorf("thinking = %v, want it off (between_tools)", req["thinking"])
	}

	// A model without server-side fallbacks gets effort only.
	if _, err := c.Complete(context.Background(), f.endpoint("sk-ant-test"), "claude-haiku-5-5", "p", 10); err != nil {
		t.Fatal(err)
	}
	reqs, headers = f.sent()
	if _, ok := reqs[1]["fallbacks"]; ok || headers[1].Get("Anthropic-Beta") != "" {
		t.Errorf("claude-haiku-5-5 was sent fallbacks: %v", reqs[1]["fallbacks"])
	}
}

// A 4,000-word chunk's request goes out, with the cap of its words and
// room to think: the SDK would refuse its max_tokens without a request
// timeout of its own.
func TestClaudeHostedChunk(t *testing.T) {
	f := newFakeClaude(t, func(map[string]any, http.Header) (int, string) {
		return 200, claudeReply("Cleaned.", "end_turn")
	})
	var c Client
	if _, err := c.Complete(context.Background(), f.endpoint("sk-ant-test"), ClaudeModel, "p", maxTokens(HostedChunkWords)); err != nil {
		t.Fatal(err)
	}
	reqs, _ := f.sent()
	if want := float64(maxTokens(HostedChunkWords) + thinkingMaxTokens); reqs[0]["max_tokens"] != want {
		t.Errorf("max_tokens %v, want %v", reqs[0]["max_tokens"], want)
	}
}

// A model that rejects effort or thinking "between_tools" gets the request
// again without them, and is sent neither from then on.
func TestClaudeEffortFallback(t *testing.T) {
	f := newFakeClaude(t, func(req map[string]any, _ http.Header) (int, string) {
		if _, ok := req["output_config"]; ok && req["model"] == "claude-haiku-4-5" {
			return 400, `{"type":"error","error":{"type":"invalid_request_error","message":"effort is not supported"}}`
		}
		return 200, claudeReply("fine", "end_turn")
	})
	var c Client
	for range 2 {
		if got, err := c.Complete(context.Background(), f.endpoint("sk-ant-test"), "claude-haiku-4-5", "p", 10); err != nil || got != "fine" {
			t.Fatalf("Complete = %q, %v", got, err)
		}
	}
	reqs, _ := f.sent()
	var with []bool
	for _, r := range reqs {
		_, effort := r["output_config"]
		_, thinking := r["thinking"]
		if effort != thinking {
			t.Errorf("effort sent %v, thinking %v; want both or neither", effort, thinking)
		}
		with = append(with, effort)
	}
	if want := []bool{true, false, false}; !slices.Equal(with, want) {
		t.Errorf("output_config sent: %v, want %v", with, want)
	}
}

func TestClaudeFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		key    string
		status int
		body   string
		want   func(error) bool
	}{
		{"refusal", "sk-ant-test", 200, claudeReply("", "refusal"), func(err error) bool { return errors.Is(err, ErrRefused) }},
		{"cut off", "sk-ant-test", 200, claudeReply("and so on and", "max_tokens"), func(err error) bool { return errors.Is(err, ErrTruncated) }},
		{"bad key", "sk-ant-wrong", 0, "", func(err error) bool {
			se, ok := errors.AsType[*StatusError](err)
			return ok && se.Status == 401 && se.Message == "invalid x-api-key"
		}},
		{"no key", "", 0, "", func(err error) bool { return errors.Is(err, ErrNoKey) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeClaude(t, func(map[string]any, http.Header) (int, string) { return tc.status, tc.body })
			var c Client
			got, err := c.Complete(context.Background(), f.endpoint(tc.key), "claude-opus-5-5", "p", 10)
			if got != "" || !tc.want(err) {
				t.Errorf("Complete = %q, %v", got, err)
			}
		})
	}
}

func TestClaudeModels(t *testing.T) {
	f := newFakeClaude(t, nil)
	var c Client
	got, err := c.Models(context.Background(), f.endpoint("sk-ant-test"))
	if err != nil || !slices.Equal(got, []string{"claude-opus-5-5", "claude-sonnet-5-5"}) {
		t.Errorf("Models = %v, %v", got, err)
	}
	if _, err := c.Models(context.Background(), f.endpoint("")); !errors.Is(err, ErrNoKey) {
		t.Errorf("Models without a key: %v, want ErrNoKey", err)
	}
}
