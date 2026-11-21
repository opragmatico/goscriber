package app

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/llm"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/ui"
)

// writeTone writes d of a tone as a WAV file and returns its path.
func writeTone(t *testing.T, name string, d time.Duration) string {
	t.Helper()
	const rate = 16000
	samples := make([]float32, int(d*rate/time.Second))
	for i := range samples {
		samples[i] = float32(0.1 * math.Sin(2*math.Pi*220*float64(i)/rate))
	}
	path := filepath.Join(t.TempDir(), name)
	if err := audio.WriteWAVFile(path, samples, rate); err != nil {
		t.Fatal(err)
	}
	return path
}

// A file transcribed from the History page lands in the history under its
// name, with its audio as the recording, after progress up to its length.
func TestTranscribeFileLandsInHistory(t *testing.T) {
	home := isolate(t)
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	path := writeTone(t, "interview.wav", 35*time.Second)

	var mu sync.Mutex
	var last, total time.Duration
	e, err := sh.historyActions().TranscribeFile(context.Background(), path, func(p ui.FileProgress) {
		mu.Lock()
		defer mu.Unlock()
		last, total = p.Done, p.Total
		if p.Parts != 0 {
			t.Errorf("post-processing progress %+v with it off", p)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if last != 35*time.Second || total != 35*time.Second {
		t.Errorf("progress ended at %v of %v", last, total)
	}
	mu.Unlock()
	// 35 s without a pause is decoded in two chunks, each "hello from the
	// test" from the fake engine.
	if e.ID == 0 || e.Source != "interview.wav" || e.Text != "hello from the test hello from the test" || e.Model != "m" || e.Duration != 35*time.Second {
		t.Fatalf("entry %+v", e)
	}
	samples, rate, err := audio.ReadWAVFile(e.Recording)
	if err != nil || rate != 16000 || len(samples) != 35*16000 {
		t.Errorf("recording %d samples at %d Hz, %v", len(samples), rate, err)
	}
	if out := sh.svc.output.(*testOutput); len(out.delivered()) != 0 {
		t.Errorf("a file's transcript was pasted: %q", out.delivered())
	}

	// With the history keeping nothing, the transcript still comes back.
	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.HistoryLimit = 0 }); err != nil {
		t.Fatal(err)
	}
	q.waitUI(t, "the new retention", func() bool { return sh.history.Retention().Limit == 0 })
	e, err = sh.transcribeFile(context.Background(), path, nil)
	if err != nil || e.ID != 0 || !strings.HasPrefix(e.Text, "hello from the test") {
		t.Errorf("with no history kept: %+v, %v", e, err)
	}
}

func TestTranscribeFileErrors(t *testing.T) {
	home := isolate(t)
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, _ := newTestShell(t, a, Options{})

	notAudio := filepath.Join(t.TempDir(), "notes.wav")
	if err := os.WriteFile(notAudio, []byte("not audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.transcribeFile(context.Background(), notAudio, nil); !errors.Is(err, audio.ErrUnsupportedFile) || strings.Contains(err.Error(), notAudio) {
		t.Errorf("a text file: %v, want ErrUnsupportedFile without the path, which the page names", err)
	}
	if _, err := sh.transcribeFile(context.Background(), filepath.Join(t.TempDir(), "gone.wav"), nil); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing file: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sh.transcribeFile(ctx, writeTone(t, "x.wav", time.Second), nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	if n, _ := sh.history.Count(context.Background()); n != 0 {
		t.Errorf("%d entries saved from failures", n)
	}
}

// fakeLLM is an OpenAI-compatible provider whose model answers every
// transcript in capitals, or fails with status when it is set.
func fakeLLM(t *testing.T, status int) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"model not loaded"}}`))
			return
		}
		prompt := req.Messages[0].Content
		text := prompt[strings.Index(prompt, "[")+1 : strings.LastIndex(prompt, "]")]
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"content": strings.ToUpper(text)}, "finish_reason": "stop",
		}}})
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

// With post-processing on, a file's transcript is cleaned by the model, with
// its progress shown, and the history keeps the model's raw text too; the
// key stored for the provider goes with each request.
func TestTranscribeFilePostProcessed(t *testing.T) {
	srv, auth := fakeLLM(t, 0)
	home := isolate(t)
	writeSettings(t, home, `{"schema_version": 2, "selected_model": "m", "llm_enabled": true, "llm_base_url": "`+srv.URL+`/v1", "llm_model": "test", "llm_prompt": "[${output}]"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, _ := newTestShell(t, a, Options{})
	if err := sh.llmActions().SetKey(srv.URL+"/v1/", "sk-test"); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var parts []ui.FileProgress
	e, err := sh.transcribeFile(context.Background(), writeTone(t, "memo.wav", 3*time.Second), func(p ui.FileProgress) {
		mu.Lock()
		defer mu.Unlock()
		if p.Parts > 0 {
			parts = append(parts, p)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.Text != "HELLO FROM THE TEST" || e.RawText != "hello from the test" {
		t.Errorf("text %q, raw %q; want the model's cleaned text and the raw transcript", e.Text, e.RawText)
	}
	mu.Lock()
	if len(parts) != 2 || parts[0].Part != 0 || parts[1].Part != 1 || parts[1].Parts != 1 || parts[1].Total != 3*time.Second {
		t.Errorf("post-processing progress %+v", parts)
	}
	mu.Unlock()
	if len(*auth) != 1 || (*auth)[0] != "Bearer sk-test" {
		t.Errorf("Authorization %q, want the stored key", *auth)
	}
}

// A provider that fails leaves a file's transcript as cleanup left it, and
// the user hears about it.
func TestTranscribeFilePostProcessingFails(t *testing.T) {
	srv, _ := fakeLLM(t, 500)
	home := isolate(t)
	writeSettings(t, home, `{"schema_version": 2, "selected_model": "m", "llm_enabled": true, "llm_base_url": "`+srv.URL+`/v1", "llm_model": "test"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, _ := newTestShell(t, a, Options{})
	e, err := sh.transcribeFile(context.Background(), writeTone(t, "memo.wav", 3*time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.Text != "Hello from the test" && e.Text != "hello from the test" {
		t.Errorf("text %q, want the transcript as cleanup left it", e.Text)
	}
	if n := sh.notices.Count(notice.CodeLLMFailed); n != 1 {
		t.Errorf("%d post-processing notices, want 1", n)
	}
}

// Process again on the History page post-processes an entry's transcript
// with the post-processing settings as they are now, even with
// llm_enabled off, and saves the model's text apart from the transcript;
// switching to a provider that fails keeps the saved text.
func TestReprocessHistoryEntry(t *testing.T) {
	srv, _ := fakeLLM(t, 0)
	down, _ := fakeLLM(t, 500)
	home := isolate(t)
	writeSettings(t, home, `{"schema_version": 2, "selected_model": "m", "llm_base_url": "`+srv.URL+`/v1", "llm_model": "test", "llm_prompt": "[${output}]"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, _ := newTestShell(t, a, Options{})
	e, err := sh.transcribeFile(context.Background(), writeTone(t, "memo.wav", 3*time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.ID == 0 || e.ProcessedText != "" || e.RawText != "hello from the test" {
		t.Fatalf("saved %+v, want an entry that was not post-processed", e)
	}

	got, err := sh.historyActions().Reprocess(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "HELLO FROM THE TEST" || got.ProcessedText != "HELLO FROM THE TEST" || got.RawText != e.RawText || got.Model != e.Model {
		t.Errorf("after processing again: %+v", got)
	}
	if saved, err := sh.history.Get(context.Background(), e.ID); err != nil || saved.ProcessedText != "HELLO FROM THE TEST" {
		t.Errorf("in the history: %+v, %v", saved, err)
	}

	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.LLMBaseURL = down.URL + "/v1" }); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.historyActions().Reprocess(context.Background(), got); err == nil {
		t.Error("a failing provider reported success")
	}
	if saved, err := sh.history.Get(context.Background(), e.ID); err != nil || saved.Text != "HELLO FROM THE TEST" || saved.ProcessedText != "HELLO FROM THE TEST" {
		t.Errorf("after a failure: %+v, %v; want the text kept", saved, err)
	}
}

// The Try button cleans the sample transcript whether post-processing is
// on or not.
func TestTryLLM(t *testing.T) {
	srv, _ := fakeLLM(t, 0)
	isolate(t)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, _ := newTestShell(t, a, Options{})
	s := config.Defaults()
	s.LLMBaseURL, s.LLMModel, s.LLMPrompt = srv.URL+"/v1", "test", "[${output}]"
	got, err := sh.llmActions().Try(context.Background(), s)
	if err != nil || got != strings.ToUpper(llm.SampleTranscript) {
		t.Errorf("Try = %q, %v", got, err)
	}
	models, err := sh.llmActions().Models(context.Background(), llm.ProviderByID(config.LLMOllama), srv.URL+"/v1")
	if err == nil || models != nil {
		t.Errorf("Models from a server without /models = %v, %v; want an error", models, err)
	}
}
