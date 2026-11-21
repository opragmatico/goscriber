package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Endpoint is a provider's API: its base URL, such as
// http://localhost:11434/v1, the key it takes, if any, and the wire format
// it speaks.
type Endpoint struct {
	BaseURL string
	// APIKey is sent as a bearer token; "" sends none, as local providers
	// want.
	APIKey string
	// API is ChatCompletions, the zero value, or Messages for Claude.
	API API
}

// url joins the base URL and an endpoint path such as "/models".
func (e Endpoint) url(path string) string { return strings.TrimRight(e.BaseURL, "/") + path }

// RequestTimeout is how long a request may take, the retry without
// reasoning_effort apart, before it counts as failed. It leaves room for
// Ollama to load a model it unloaded (3.6 s for gemma4:26b on an M4 Max)
// and for a 400-word chunk (69 s for 13 of them).
const RequestTimeout = 30 * time.Second

// ThinkingTimeout is how long a request to a model that thinks at a set
// effort (modelEfforts) may take: thinking at "medium" comes before the
// answer, and GPT-6.1 Sol writes at about 40 tokens a second.
const ThinkingTimeout = 2 * time.Minute

// maxResponse caps how much of a response is read.
const maxResponse = 4 << 20

// Errors a completion fails with, besides *StatusError and the transport's.
var (
	// ErrTruncated: the response stopped at the output cap
	// (finish_reason "length"), as a model that repeats itself does.
	ErrTruncated = errors.New("the model's answer stopped at the output limit")
	// ErrNoChoice: the response held no answer.
	ErrNoChoice = errors.New("the response held no answer")
)

// StatusError is a response with an HTTP error status.
type StatusError struct {
	Status int
	// Message is the provider's own explanation, if it gave one.
	Message string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("the server answered %d %s", e.Status, http.StatusText(e.Status))
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// rejected reports whether err is a 400 or 422, the statuses a server
// answers a request whose fields it does not take with.
func rejected(err error) bool {
	se, ok := errors.AsType[*StatusError](err)
	return ok && (se.Status == http.StatusBadRequest || se.Status == http.StatusUnprocessableEntity)
}

// Client talks to providers through Chat Completions, and to Claude
// through its Messages API (claude.go). It is safe for concurrent use, and
// remembers how far each base URL and model pair let thinking be turned
// down, so keep one for the life of the app.
type Client struct {
	// HTTP sends the requests; nil means http.DefaultClient.
	HTTP *http.Client
	// Timeout bounds each request; 0 means RequestTimeout.
	Timeout time.Duration

	mu sync.Mutex
	// effort is, by base URL + "\x00" + model, the index in efforts of the
	// first reasoning_effort the pair did not reject; for Claude, 1 once
	// the model rejected the extras of claudeRequest.
	effort map[string]int
}

// efforts are the values of reasoning_effort tried in turn, least thinking
// first, until a server takes one, for a model not in modelEfforts: "none"
// turns thinking off on Ollama and Gemini 2.5 Flash; "minimal" and "low"
// are for models that reject "none", such as gpt-oss-120b on Groq, which
// takes "low" at the least; "" leaves the field out, for servers that know
// none of them. A value taken is not always thinking turned off: Gemini 2.5 Pro,
// Gemini 3 and Gemini 4 think whatever they are asked (gemini-4-hydrogen
// takes "none" and still thinks for about 1,000 tokens on a 400-word
// chunk), so their requests leave room for it (thinksAnyway).
var efforts = []string{"none", "minimal", "low", ""}

// modelEfforts are models asked to think at a set reasoning_effort instead
// of as little as they allow: they skip the step-down through efforts, and
// their requests leave room for the thinking, with an output cap of at
// least thinkingMaxTokens and ThinkingTimeout.
var modelEfforts = map[string]string{
	OpenAIModel: "medium",
}

// thinkingMaxTokens is the least output cap of a request to a model that
// thinks: the thinking counts toward the cap, so it leaves room for it on
// top of the answer; the request timeout still bounds a model that runs on.
const thinkingMaxTokens = 16000

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return RequestTimeout
}

// thinkingTimeout bounds a request to a model in modelEfforts.
func (c *Client) thinkingTimeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return ThinkingTimeout
}

func effortKey(ep Endpoint, model string) string {
	return strings.TrimRight(ep.BaseURL, "/") + "\x00" + model
}

// effortFor is the index in efforts to start from for key.
func (c *Client) effortFor(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.effort[key]
}

func (c *Client) rememberEffort(key string, i int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.effort == nil {
		c.effort = map[string]int{}
	}
	c.effort[key] = i
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
	// ReasoningEffort "none" turns thinking off; a model that thinks first
	// is far too slow for dictation.
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	MaxTokens           int    `json:"max_tokens,omitempty"`
	MaxCompletionTokens int    `json:"max_completion_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// Complete sends prompt to model as one user message and returns the
// answer, which may be at most maxTokens long: an answer cut off there
// fails with ErrTruncated. Thinking is turned as far down as the model
// allows: with reasoning_effort "none", and when the server rejects the
// request with it (400 or 422), "minimal", "low", then no reasoning_effort. The
// first value that gets past the server's checks is the one the client
// starts from for this base URL and model from then on. A model in
// modelEfforts is asked with its effort instead, and given room to think.
func (c *Client) Complete(ctx context.Context, ep Endpoint, model, prompt string, maxTokens int) (string, error) {
	if ep.API == Messages {
		return c.claudeComplete(ctx, ep, model, prompt, maxTokens)
	}
	req := chatRequest{
		Model:    model,
		Messages: []message{{Role: "user", Content: prompt}},
	}
	effort, thinks := modelEfforts[model]
	if thinks || thinksAnyway(ep.BaseURL) {
		maxTokens = max(maxTokens, thinkingMaxTokens)
	}
	// OpenAI's own API takes max_completion_tokens from every chat model,
	// and only that from its reasoning models; max_tokens is what the rest
	// of the providers know.
	if isOpenAI(ep.BaseURL) {
		req.MaxCompletionTokens = maxTokens
	} else {
		req.MaxTokens = maxTokens
	}
	if thinks {
		req.ReasoningEffort = effort
		return c.chat(ctx, ep, req, c.thinkingTimeout())
	}
	key := effortKey(ep, model)
	start := c.effortFor(key)
	var answer string
	var err error
	for i := start; i < len(efforts); i++ {
		req.ReasoningEffort = efforts[i]
		answer, err = c.chat(ctx, ep, req, c.timeout())
		if !rejected(err) {
			if i != start {
				c.rememberEffort(key, i)
			}
			break
		}
	}
	return answer, err
}

func (c *Client) chat(ctx context.Context, ep Endpoint, body chatRequest, timeout time.Duration) (string, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	var resp chatResponse
	if err := c.doWithin(ctx, timeout, ep, http.MethodPost, "/chat/completions", data, &resp); err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", ErrNoChoice
	}
	choice := resp.Choices[0]
	if choice.FinishReason == "length" {
		return "", ErrTruncated
	}
	return choice.Message.Content, nil
}

// Models lists the models the provider serves, sorted.
func (c *Client) Models(ctx context.Context, ep Endpoint) ([]string, error) {
	if ep.API == Messages {
		return c.claudeModels(ctx, ep)
	}
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, ep, http.MethodGet, "/models", nil, &resp); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		id := m.ID
		if isGemini(ep.BaseURL) {
			// Gemini lists "models/gemini-4-hydrogen", and takes the model
			// with or without the prefix.
			id = strings.TrimPrefix(id, "models/")
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// do sends a request with a JSON body, if any, and decodes the JSON answer
// into out.
func (c *Client) do(ctx context.Context, ep Endpoint, method, path string, body []byte, out any) error {
	return c.doWithin(ctx, c.timeout(), ep, method, path, body, out)
}

// doWithin is do with a timeout of its own.
func (c *Client) doWithin(ctx context.Context, timeout time.Duration, ep Endpoint, method, path string, body []byte, out any) error {
	if _, err := ParseBaseURL(ep.BaseURL); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, ep.url(path), r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if ep.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+ep.APIKey)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("no answer within %s: %w", timeout, context.DeadlineExceeded)
		}
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Status: resp.StatusCode, Message: errorMessage(data)}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("the answer is not the JSON expected: %w", err)
	}
	return nil
}

// errorMessage digs the explanation out of an error response:
// {"error": {"message": "..."}} as OpenAI writes it, {"error": "..."} as
// some others do, or a short plain-text body.
func errorMessage(data []byte) string {
	var body struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &body) == nil && len(body.Error) > 0 {
		var obj struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body.Error, &obj) == nil && obj.Message != "" {
			return obj.Message
		}
		var s string
		if json.Unmarshal(body.Error, &s) == nil {
			return s
		}
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 200 || strings.HasPrefix(s, "<") || strings.HasPrefix(s, "{") {
		return ""
	}
	return s
}

// ParseBaseURL checks that s is an http or https URL with a host.
func ParseBaseURL(s string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%q is not an http or https URL", s)
	}
	return u, nil
}

// isOpenAI reports whether baseURL is OpenAI's own API.
func isOpenAI(baseURL string) bool { return hostIs(baseURL, "api.openai.com") }

// isGemini reports whether baseURL is Gemini's API.
func isGemini(baseURL string) bool { return hostIs(baseURL, "generativelanguage.googleapis.com") }

// thinksAnyway reports whether the models at baseURL think however little
// reasoning_effort asks for, so a request's output cap must leave room for
// the thinking, which counts toward it: Gemini's, from 2.5 Pro on.
func thinksAnyway(baseURL string) bool { return isGemini(baseURL) }

func hostIs(baseURL, host string) bool {
	u, err := ParseBaseURL(baseURL)
	return err == nil && strings.EqualFold(u.Hostname(), host)
}

// IsLocal reports whether baseURL is a server on this computer, such as
// Ollama or LM Studio, for which Goscriber suggests a model.
func IsLocal(baseURL string) bool {
	u, err := ParseBaseURL(baseURL)
	if err != nil {
		return false
	}
	switch h := strings.ToLower(u.Hostname()); {
	case h == "localhost", strings.HasSuffix(h, ".localhost"), h == "::1":
		return true
	default:
		return strings.HasPrefix(h, "127.")
	}
}
