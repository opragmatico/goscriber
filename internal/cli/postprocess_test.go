package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/opragmatico/goscriber/engine/fake"
	"github.com/opragmatico/goscriber/internal/config"
)

// llmServer is an OpenAI-compatible provider serving the model "test",
// which answers each transcript in capitals, or with status when it is set.
type llmServer struct {
	*httptest.Server
	status int

	mu    sync.Mutex
	chats int
	auth  []string
}

func newLLMServer(t *testing.T, status int) *llmServer {
	t.Helper()
	s := &llmServer{status: status}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"test"},{"id":"other"}]}`))
			return
		}
		s.chats++
		if s.status != 0 {
			w.WriteHeader(s.status)
			_, _ = w.Write([]byte(`{"error":{"message":"out of memory"}}`))
			return
		}
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		p := req.Messages[0].Content
		var text string
		if i := strings.Index(p, "["); i >= 0 {
			text = p[i+1 : strings.LastIndex(p, "]")]
		} else { // the built-in prompt
			text = p[len("<transcript>\n"):strings.Index(p, "\n</transcript>")]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"content": strings.ToUpper(text)}, "finish_reason": "stop",
		}}})
	}))
	t.Cleanup(s.Close)
	return s
}

// withLLMSettings saves settings whose post-processing points at srv, with
// a prompt that brackets the transcript, off.
func withLLMSettings(t *testing.T, srv *llmServer) {
	t.Helper()
	paths, _ := config.ResolvePaths()
	s := config.Defaults()
	s.LLMBaseURL, s.LLMModel, s.LLMPrompt = srv.URL+"/v1", "test", "[${output}]"
	if err := config.Save(paths.SettingsFile(), s); err != nil {
		t.Fatal(err)
	}
}

func TestTranscribeLLM(t *testing.T) {
	srv := newLLMServer(t, 0)
	s := newTranscribe(t, fake.Script{Replies: []fake.Reply{fake.Transcript("ask not what your country can do<unk> for you")}})
	withLLMSettings(t, srv)
	var asked []string
	s.env.LLMKey = func(baseURL string) (string, error) {
		asked = append(asked, baseURL)
		return "sk-test", nil
	}
	code, stdout, stderr := runWith(t, s.env, "transcribe", "--llm", fixture)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if stdout != "ASK NOT WHAT YOUR COUNTRY CAN DO: FOR YOU\n" {
		t.Errorf("stdout %q, want the cleaned transcript", stdout)
	}
	if len(asked) != 1 || asked[0] != srv.URL+"/v1" || srv.auth[len(srv.auth)-1] != "Bearer sk-test" {
		t.Errorf("keys asked for %v, sent %v", asked, srv.auth)
	}
}

func TestTranscribeLLMJSON(t *testing.T) {
	srv := newLLMServer(t, 0)
	s := newTranscribe(t, fake.Script{Replies: []fake.Reply{fake.Transcript("ask not")}})
	s.env.LLMKey = func(string) (string, error) { return "", nil }
	// The flags stand in for the settings.
	code, stdout, stderr := runWith(t, s.env, "transcribe", "--json", "--llm-url", srv.URL+"/v1", "--llm-model", "other", fixture)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	var got jsonResult
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatal(err)
	}
	if got.Text != "ASK NOT" || got.RawText != "ask not" || got.LLMModel != "other" {
		t.Errorf("text %.40q, raw %q, model %q", got.Text, got.RawText, got.LLMModel)
	}
}

// A provider that is down, or lacks the model, fails before any audio is
// decoded; so does a hosted provider with no model chosen.
func TestTranscribeLLMRefusals(t *testing.T) {
	srv := newLLMServer(t, 0)
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			"missing model",
			[]string{"--llm-url", srv.URL + "/v1", "--llm-model", "gemma4:e4b"},
			"does not serve gemma4:e4b (with Ollama: ollama pull gemma4:e4b); it serves other, test, which --llm-model can pick",
		},
		{"server down", []string{"--llm-url", down.URL + "/v1", "--llm-model", "test"}, "cannot be reached"},
		{"no model for a hosted provider", []string{"--llm-url", "https://api.groq.com/openai/v1"}, "no model is chosen for https://api.groq.com/openai/v1; choose one with --llm-model"},
		{"bad URL", []string{"--llm-url", "localhost:11434"}, "--llm-url:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTranscribe(t, fake.Script{})
			s.env.LLMKey = func(string) (string, error) { return "", nil }
			code, _, stderr := runWith(t, s.env, append(append([]string{"transcribe"}, tc.args...), fixture)...)
			if code != exitFailure || !strings.Contains(stderr, tc.want) {
				t.Errorf("exit %d, stderr %q; want %q", code, stderr, tc.want)
			}
			if len(s.eng.Loads()) != 0 {
				t.Error("the speech model was loaded")
			}
		})
	}
}

// A model that fails leaves the transcript as the speech model wrote it,
// says so and exits 1.
func TestTranscribeLLMFails(t *testing.T) {
	srv := newLLMServer(t, 500)
	s := newTranscribe(t, fake.Script{Replies: []fake.Reply{fake.Transcript("ask not")}})
	withLLMSettings(t, srv)
	s.env.LLMKey = func(string) (string, error) { return "", nil }
	code, stdout, stderr := runWith(t, s.env, "transcribe", "--llm", fixture)
	if code != exitFailure || stdout != "ask not\n" ||
		!strings.Contains(stderr, "the language model failed on 1 of 1 parts, printed as transcribed: the server answered 500 Internal Server Error: out of memory") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// Without --llm nothing is sent, whatever the settings say, and <unk> is
// replaced all the same.
func TestTranscribeWithoutLLM(t *testing.T) {
	srv := newLLMServer(t, 0)
	s := newTranscribe(t, fake.Script{Replies: []fake.Reply{fake.Transcript("Note<unk> up 10<unk>")}})
	withLLMSettings(t, srv)
	code, stdout, stderr := runWith(t, s.env, "transcribe", fixture)
	if code != 0 || stdout != "Note: up 10%\n" {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if len(srv.auth) != 0 {
		t.Errorf("%d requests to the provider", len(srv.auth))
	}
}
