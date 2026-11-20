package llm

// Provider is an OpenAI-compatible provider whose base URL Goscriber
// knows, for the settings to offer.
type Provider struct {
	Name    string
	BaseURL string
	// Local: it runs on this computer and takes no key.
	Local bool
}

// Providers are the providers the settings offer, local ones first; any
// other base URL can be typed in.
var Providers = []Provider{
	{"Ollama", "http://localhost:11434/v1", true},
	{"LM Studio", "http://localhost:1234/v1", true},
	{"llama.cpp (llama-server)", "http://localhost:8080/v1", true},
	{"OpenAI", "https://api.openai.com/v1", false},
	{"OpenRouter", "https://openrouter.ai/api/v1", false},
	{"Groq", "https://api.groq.com/openai/v1", false},
}

// ProviderFor returns the provider whose base URL is baseURL, trailing
// slash aside.
func ProviderFor(baseURL string) (Provider, bool) {
	account := KeyAccount(baseURL)
	for _, p := range Providers {
		if p.BaseURL == account {
			return p, true
		}
	}
	return Provider{}, false
}

// SampleTranscript is what the settings' Try button has the model clean:
// a dictation with the errors a speech model makes.
const SampleTranscript = "um so the meeting is on tuesday at three thirty and uh we need twenty five percent more budget " +
	"for the get hub actions comma right question mark"
