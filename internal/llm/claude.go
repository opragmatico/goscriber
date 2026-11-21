package llm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// Claude is spoken to through Anthropic's SDK and its Messages API, not an
// OpenAI-compatible endpoint.

// ErrRefused is the failure of a request Claude declined (stop_reason
// "refusal"); the transcript keeps its text.
var ErrRefused = errors.New("the model declined to answer")

// ErrNoKey is the failure of a request to a provider that needs an API key
// when none is stored.
var ErrNoKey = errors.New("no API key is stored for this provider")

// fallbackModels take server-side fallbacks: a request one of them declines
// is answered by another model in the same call, instead of failing.
var fallbackModels = []string{
	"claude-fable-5-2", "claude-fable-5-1", "claude-mythos-5-2", "claude-mythos-5-1",
	"claude-opus-5-5", "claude-opus-5", "claude-sonnet-5-5",
}

func (c *Client) claude(ep Endpoint) anthropic.Client {
	return anthropic.NewClient(
		option.WithAPIKey(ep.APIKey),
		option.WithBaseURL(ep.BaseURL),
		option.WithHTTPClient(c.http()),
	)
}

// claudeRequest is the request for prompt. With extras it turns thinking
// off and asks for the least effort, and, from a model that takes them,
// server-side fallbacks; models older than these reject them. Current
// models turn thinking off with thinking "between_tools", which thinks only
// between tool calls, and this request has none; they reject "disabled".
func claudeRequest(model, prompt string, maxTokens int, extras bool) anthropic.BetaMessageNewParams {
	req := anthropic.BetaMessageNewParams{
		Model: model,
		// Room for thinking, for a model sent no extras, which thinks as it
		// sees fit.
		MaxTokens: int64(maxTokens + thinkingMaxTokens),
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(prompt))},
	}
	if extras {
		req.Thinking = anthropic.BetaThinkingConfigParamUnion{OfBetweenTools: &anthropic.BetaThinkingConfigBetweenToolsParam{}}
		req.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortLow}
		if slices.Contains(fallbackModels, model) {
			req.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
			req.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		}
	}
	return req
}

// claudeComplete is Complete for Claude. A model that rejects the extras
// of claudeRequest (400) gets the request again without them, and is sent
// none from then on.
func (c *Client) claudeComplete(ctx context.Context, ep Endpoint, model, prompt string, maxTokens int) (string, error) {
	if ep.APIKey == "" {
		return "", ErrNoKey
	}
	key := effortKey(ep, model)
	extras := c.effortFor(key) == 0
	timeout := c.requestTimeout(maxTokens, false)
	msg, err := c.claudeNew(ctx, ep, claudeRequest(model, prompt, maxTokens, extras), timeout)
	if extras && rejected(err) {
		msg, err = c.claudeNew(ctx, ep, claudeRequest(model, prompt, maxTokens, false), timeout)
		if !rejected(err) {
			c.rememberEffort(key, 1)
		}
	}
	if err != nil {
		return "", err
	}
	switch msg.StopReason {
	case anthropic.BetaStopReasonMaxTokens:
		return "", ErrTruncated
	case anthropic.BetaStopReasonRefusal:
		return "", ErrRefused
	}
	var text strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	return text.String(), nil
}

func (c *Client) claudeNew(ctx context.Context, ep Endpoint, req anthropic.BetaMessageNewParams, timeout time.Duration) (*anthropic.BetaMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cl := c.claude(ep)
	// With a timeout of its own the SDK does not refuse, as needing
	// streaming, a request whose max_tokens it guesses could take more
	// than 10 minutes, as a 4,000-word chunk's does.
	msg, err := cl.Beta.Messages.New(ctx, req, option.WithRequestTimeout(timeout))
	return msg, c.claudeError(ctx, err, timeout)
}

// claudeModels is Models for Claude.
func (c *Client) claudeModels(ctx context.Context, ep Endpoint) ([]string, error) {
	if ep.APIKey == "" {
		return nil, ErrNoKey
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	cl := c.claude(ep)
	pager := cl.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
	var ids []string
	for pager.Next() {
		if id := pager.Current().ID; id != "" {
			ids = append(ids, id)
		}
	}
	if err := c.claudeError(ctx, pager.Err(), c.timeout()); err != nil {
		return nil, err
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// claudeError turns the SDK's errors into the client's own: an HTTP error
// status into *StatusError, and a request that ran past the timeout into
// the same error Chat Completions gives.
func (c *Client) claudeError(ctx context.Context, err error, timeout time.Duration) error {
	if err == nil {
		return nil
	}
	if apiErr, ok := errors.AsType[*anthropic.Error](err); ok {
		return &StatusError{Status: apiErr.StatusCode, Message: apiErr.Message}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("no answer within %s: %w", timeout, context.DeadlineExceeded)
	}
	return err
}
