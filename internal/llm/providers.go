package llm

import (
	"strings"

	"github.com/opragmatico/goscriber/internal/config"
)

// API is the wire format a provider is spoken to in.
type API int

const (
	// ChatCompletions is OpenAI's Chat Completions, which every provider
	// but Claude offers.
	ChatCompletions API = iota
	// Messages is Anthropic's Messages API, spoken through Anthropic's SDK.
	Messages
)

// KeyNeed says whether a provider takes an API key.
type KeyNeed int

const (
	// NoKey is a provider on the user's own computer or network.
	NoKey KeyNeed = iota
	// KeyRequired is a provider every request to which needs a key.
	KeyRequired
	// KeyOptional is a server of the user's own, which may or may not want one.
	KeyOptional
)

// Provider is a service the settings offer for post-processing.
type Provider struct {
	ID   config.LLMProvider
	Name string
	// BaseURL is where it is reached unless the user types another address,
	// which only EditableURL providers take.
	BaseURL     string
	EditableURL bool
	Key         KeyNeed
	// KeyHelp says where to create a key, for providers that take one.
	KeyHelp string
	API     API
	// Model is the model used while none is chosen, "" for none; a provider
	// on this computer gets the one suggested for its memory instead.
	Model string
}

// The models the hosted providers post-process with while none is chosen:
// each provider's model at the level of Claude Sonnet, in price and in how
// well it fixes a transcript.
const (
	ClaudeModel = "claude-sonnet-5-5"
	// GeminiModel is asked to think at "medium" (modelEfforts).
	GeminiModel = "gemini-4-hydrogen"
	// OpenAIModel is priced as Claude Sonnet 5.5 is, and asked to think
	// at "medium" (modelEfforts).
	OpenAIModel = "gpt-6.1-sol"
)

// Providers are the providers the settings offer, in their order.
var Providers = []Provider{
	{
		ID: config.LLMGemini, Name: "Gemini", BaseURL: config.LLMGemini.BaseURL(), Key: KeyRequired,
		KeyHelp: "Create one in Google AI Studio, under Get API key.", Model: GeminiModel,
	},
	{
		ID: config.LLMOpenAI, Name: "OpenAI", BaseURL: config.LLMOpenAI.BaseURL(), Key: KeyRequired,
		KeyHelp: "Create one on the OpenAI Platform, under API keys.", Model: OpenAIModel,
	},
	{
		ID: config.LLMClaude, Name: "Claude", BaseURL: config.LLMClaude.BaseURL(), Key: KeyRequired,
		KeyHelp: "Create one in the Claude Console, under API keys.", API: Messages, Model: ClaudeModel,
	},
	{
		ID: config.LLMOpenRouter, Name: "OpenRouter", BaseURL: config.LLMOpenRouter.BaseURL(), Key: KeyRequired,
		KeyHelp: "Create one on OpenRouter, under Keys.",
	},
	{
		ID: config.LLMGroq, Name: "Groq", BaseURL: config.LLMGroq.BaseURL(), Key: KeyRequired,
		KeyHelp: "Create one in the GroqCloud console, under API Keys.",
	},
	{ID: config.LLMOllama, Name: "Ollama", BaseURL: config.LLMOllama.BaseURL(), EditableURL: true},
	{ID: config.LLMCustom, Name: "Other (OpenAI-compatible)", EditableURL: true, Key: KeyOptional},
}

// ProviderByID returns the provider id names; an unknown id is the custom
// provider.
func ProviderByID(id config.LLMProvider) Provider {
	for _, p := range Providers {
		if p.ID == id {
			return p
		}
	}
	return ProviderByID(config.LLMCustom)
}

// ProviderOf returns the provider the settings s post-process with.
func ProviderOf(s config.Settings) Provider { return ProviderByID(s.LLMProvider) }

// URL is where the provider is reached with the settings s: the base URL
// typed in, for a provider that takes one, else its own.
func (p Provider) URL(s config.Settings) string {
	if p.EditableURL {
		return strings.TrimSpace(s.LLMBaseURL)
	}
	return p.BaseURL
}

// Endpoint is the provider's API at baseURL, with key.
func (p Provider) Endpoint(baseURL, key string) Endpoint {
	return Endpoint{BaseURL: baseURL, APIKey: key, API: p.API}
}

// SampleTranscript is what the settings' Try button has the model clean:
// a dictation with the errors a speech model makes.
const SampleTranscript = "um so the meeting is on tuesday at three thirty and uh we need twenty five percent more budget " +
	"for the get hub actions comma right question mark"
