package goscriber

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/hook"
	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/fake"
	"github.com/opragmatico/goscriber/internal/engine/worker"
	"github.com/opragmatico/goscriber/internal/models"
	"github.com/opragmatico/goscriber/internal/pipeline"
)

// envWorker makes the test binary an engine worker hosting a fake engine, as
// a goscriber binary runs the real one: see TestWorkerProcess.
const envWorker = "GOSCRIBER_TEST_ENGINE_WORKER"

// workerText is what the fake engine in the worker process hears.
const workerText = "um, transcribed in another process"

func TestMain(m *testing.M) {
	if os.Getenv(envWorker) != "" {
		eng := fake.New(fake.Script{Replies: []fake.Reply{fake.Transcript(workerText)}})
		if err := worker.Run(eng); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const rate = 16000

// speech is audio the speech gate lets through: a tone, loud enough.
func speech(d time.Duration) []float32 {
	s := make([]float32, int(d.Seconds()*rate))
	for i := range s {
		s[i] = float32(0.2 * math.Sin(2*math.Pi*440*float64(i)/rate))
	}
	return s
}

// fakeSource resolves to one model.
type fakeSource struct{ model pipeline.Model }

func (f fakeSource) Resolve(config.Settings) (pipeline.Model, error) { return f.model, nil }

var testModel = pipeline.Model{
	Spec:     engine.LoadSpec{ID: "parakeet-tdt-0.6b-v2", Path: "/models/asr.gguf"},
	Name:     "Parakeet TDT 0.6B v2",
	Language: "en-US",
}

func newTest(t *testing.T, eng engine.Engine) *Transcriber {
	t.Helper()
	tr := newTranscriber(eng, fakeSource{testModel}, config.Defaults(), slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func TestTranscribe(t *testing.T) {
	eng := fake.New(fake.Script{Replies: []fake.Reply{fake.Transcript("Um, hello from the library.")}})
	tr := newTest(t, eng)
	var mu sync.Mutex
	var progress []time.Duration
	got, err := tr.Transcribe(context.Background(), speech(2*time.Second), rate, OnProgress(func(done, total time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		if total != 2*time.Second {
			t.Errorf("progress total %v", total)
		}
		progress = append(progress, done)
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Transcript{
		Text:     "Hello from the library.",
		RawText:  "Um, hello from the library.",
		Model:    "parakeet-tdt-0.6b-v2",
		Language: "en-US",
		Duration: 2 * time.Second,
	}
	if got != want {
		t.Errorf("Transcribe = %+v\nwant %+v", got, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(progress) == 0 || progress[len(progress)-1] != 2*time.Second {
		t.Errorf("progress %v, want it to end at the audio's length", progress)
	}
	if loads := eng.Loads(); len(loads) != 1 || loads[0].ID != "parakeet-tdt-0.6b-v2" {
		t.Errorf("loads %+v", loads)
	}
}

func TestTranscribeKeepsTheModelLoaded(t *testing.T) {
	eng := fake.New(fake.Script{Replies: []fake.Reply{fake.Transcript("one"), fake.Transcript("two")}})
	tr := newTest(t, eng)
	for _, want := range []string{"one", "two"} {
		got, err := tr.Transcribe(context.Background(), speech(time.Second), rate)
		if err != nil || got.Text != want {
			t.Errorf("Transcribe = %q, %v; want %q", got.Text, err, want)
		}
	}
	if n := len(eng.Loads()); n != 1 {
		t.Errorf("%d loads, want the model loaded once", n)
	}
}

func TestTranscribeConcurrently(t *testing.T) {
	tr := newTest(t, fake.New(fake.Script{Replies: []fake.Reply{fake.Transcript("same words")}}))
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			got, err := tr.Transcribe(context.Background(), speech(time.Second), rate)
			if err != nil || got.Text != "same words" {
				t.Errorf("Transcribe = %q, %v", got.Text, err)
			}
		})
	}
	wg.Wait()
}

func TestTranscribeErrors(t *testing.T) {
	tr := newTest(t, fake.New(fake.Script{Replies: []fake.Reply{fake.Transcript("words")}}))
	ctx := context.Background()
	if _, err := tr.Transcribe(ctx, make([]float32, rate), rate); !errors.Is(err, ErrNoSpeech) {
		t.Errorf("silence: %v, want ErrNoSpeech", err)
	}
	if _, err := tr.Transcribe(ctx, speech(time.Second), 4000); !errors.Is(err, ErrUnsupportedAudio) {
		t.Errorf("4 kHz: %v, want ErrUnsupportedAudio", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := tr.Transcribe(cancelled, speech(time.Second), rate); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v, want context.Canceled", err)
	}

	broken := newTest(t, fake.New(fake.Script{LoadErr: engine.ErrModelNotFound}))
	if _, err := broken.Transcribe(ctx, speech(time.Second), rate); !errors.Is(err, engine.ErrModelNotFound) {
		t.Errorf("model that does not load: %v", err)
	}
}

func TestTranscribeFileAndReader(t *testing.T) {
	tr := newTest(t, fake.New(fake.Script{Replies: []fake.Reply{fake.Transcript("from a file")}}))
	ctx := context.Background()
	dir := t.TempDir()
	wav := filepath.Join(dir, "memo.wav")
	if err := audio.WriteWAVFile(wav, speech(1500*time.Millisecond), rate); err != nil {
		t.Fatal(err)
	}
	got, err := tr.TranscribeFile(ctx, wav)
	if err != nil || got.Text != "from a file" || got.Duration != 1500*time.Millisecond {
		t.Errorf("TranscribeFile = %+v, %v", got, err)
	}

	var buf bytes.Buffer
	if err := audio.WriteWAV(&buf, speech(time.Second), rate); err != nil {
		t.Fatal(err)
	}
	if got, err := tr.TranscribeReader(ctx, &buf); err != nil || got.Text != "from a file" {
		t.Errorf("TranscribeReader = %+v, %v", got, err)
	}

	mp3 := filepath.Join(dir, "memo.mp3")
	if err := os.WriteFile(mp3, append([]byte{0xFF, 0xFB, 0x90, 0x64}, make([]byte, 2048)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.TranscribeFile(ctx, mp3); !errors.Is(err, ErrUnsupportedAudio) {
		t.Errorf("MP3: %v, want ErrUnsupportedAudio", err)
	}
	if _, err := tr.TranscribeReader(ctx, bytes.NewReader([]byte("plain text, not audio"))); !errors.Is(err, ErrUnsupportedAudio) {
		t.Errorf("text: %v, want ErrUnsupportedAudio", err)
	}
	if _, err := tr.TranscribeFile(ctx, filepath.Join(dir, "missing.wav")); !errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrUnsupportedAudio) {
		t.Errorf("missing file: %v, want fs.ErrNotExist", err)
	}
}

func TestClose(t *testing.T) {
	eng := fake.New(fake.Script{Replies: []fake.Reply{fake.Transcript("words")}})
	tr := newTest(t, eng)
	if _, err := tr.Transcribe(context.Background(), speech(time.Second), rate); err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if eng.Loaded() {
		t.Error("the model is still loaded after Close")
	}
	if _, err := tr.Transcribe(context.Background(), speech(time.Second), rate); !errors.Is(err, ErrClosed) {
		t.Errorf("Transcribe after Close: %v, want ErrClosed", err)
	}
	if err := tr.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}
}

func TestTranscriptHookInfo(t *testing.T) {
	tr := Transcript{Text: "olá", Model: "nemotron-3.5-asr-streaming-0.6b", Language: "pt-BR"}
	if got, want := tr.HookInfo(), (hook.Info{Model: "nemotron-3.5-asr-streaming-0.6b", Language: "pt-BR"}); got != want {
		t.Errorf("HookInfo = %+v, want %+v", got, want)
	}
}

// TestTranscriptJSON pins the JSON names, which workflow states and API
// responses depend on.
func TestTranscriptJSON(t *testing.T) {
	tr := Transcript{Text: "Olá.", RawText: "olá", Model: "nemotron-3.5-asr-streaming-0.6b", Language: "pt-BR", Duration: 1500 * time.Millisecond}
	b, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"text":"Olá.","raw_text":"olá","model":"nemotron-3.5-asr-streaming-0.6b","language":"pt-BR","duration_ns":1500000000}`
	if string(b) != want {
		t.Errorf("JSON %s\nwant %s", b, want)
	}
	var back Transcript
	if err := json.Unmarshal(b, &back); err != nil || back != tr {
		t.Errorf("round trip: %+v, %v", back, err)
	}
}

func TestApply(t *testing.T) {
	cat := models.Default()
	base := config.Defaults()
	base.SelectedModel = "parakeet-tdt-0.6b-v2"
	base.Language = "en-US"
	base.CustomWords = []string{"Goscriber"}

	s, err := apply(Options{}, base, cat)
	if err != nil || s.SelectedModel != "parakeet-tdt-0.6b-v2" || s.Language != "en-US" || !slices.Equal(s.CustomWords, []string{"Goscriber"}) {
		t.Errorf("no options: %+v, %v; want the settings", s, err)
	}

	s, err = apply(Options{Model: "nemotron-3.5-asr-streaming-0.6b", Language: "PT-br", Words: []string{"Queequeg"}}, base, cat)
	if err != nil || s.SelectedModel != "nemotron-3.5-asr-streaming-0.6b" || s.Language != "PT-br" ||
		!slices.Equal(s.CustomWords, []string{"Goscriber", "Queequeg"}) {
		t.Errorf("options: %+v, %v", s, err)
	}
	if !slices.Equal(base.CustomWords, []string{"Goscriber"}) {
		t.Errorf("apply changed the settings it was given: %v", base.CustomWords)
	}

	if s, err := apply(Options{}, config.Defaults(), cat); err != nil || s.SelectedModel != cat.DefaultModel {
		t.Errorf("no model selected: %q, %v; want the catalog's default", s.SelectedModel, err)
	}
	if _, err := apply(Options{Model: "whisper-large"}, base, cat); !errors.Is(err, ErrNoModel) {
		t.Errorf("unknown model: %v, want ErrNoModel", err)
	}
	if _, err := apply(Options{Model: "nemotron-3.5-asr-streaming-0.6b", Language: "xx-YY"}, base, cat); err == nil {
		t.Error("a language the model does not take was accepted")
	}
	if s, err := apply(Options{Language: "de-DE"}, base, cat); err != nil || s.Language != "de-DE" {
		t.Errorf("a language for an English-only model: %v; want it passed on for the model to ignore", err)
	}
}

func TestNewWithoutTheModel(t *testing.T) {
	t.Setenv(config.HomeEnv, t.TempDir())
	_, err := New(Options{Model: "parakeet-tdt-0.6b-v2", Executable: os.Args[0]})
	if !errors.Is(err, ErrNoModel) {
		t.Errorf("New = %v, want ErrNoModel", err)
	}
}

func TestNewEngine(t *testing.T) {
	saved := inProcessEngine
	t.Cleanup(func() { inProcessEngine = saved })
	inProcessEngine = nil
	log := slog.New(slog.DiscardHandler)

	if _, err := newEngine(Options{InProcess: true}, log); !errors.Is(err, ErrNoEngine) {
		t.Errorf("in process without the engine: %v, want ErrNoEngine", err)
	}
	dir := t.TempDir()
	if _, err := newEngine(Options{Executable: filepath.Join(dir, "goscriber")}, log); !errors.Is(err, ErrNoEngine) {
		t.Errorf("missing executable: %v, want ErrNoEngine", err)
	}
	if _, err := newEngine(Options{Executable: dir}, log); !errors.Is(err, ErrNoEngine) {
		t.Errorf("a directory: %v, want ErrNoEngine", err)
	}
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newEngine(Options{Executable: plain}, log); runtime.GOOS != "windows" && !errors.Is(err, ErrNoEngine) {
		t.Errorf("a file without execute permission: %v, want ErrNoEngine", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := newEngine(Options{}, log); !errors.Is(err, ErrNoEngine) {
		t.Errorf("no goscriber in PATH: %v, want ErrNoEngine", err)
	}

	inProcessEngine = func() engine.Engine { return fake.New(fake.Script{}) }
	if eng, err := newEngine(Options{InProcess: true}, log); err != nil || eng == nil {
		t.Errorf("in process: %v, %v", eng, err)
	}
}

// TestWorkerProcess transcribes through an engine worker process, as with a
// goscriber binary: here the test binary, hosting a fake engine (TestMain).
func TestWorkerProcess(t *testing.T) {
	t.Setenv(envWorker, "1")
	eng, err := newEngine(Options{Executable: os.Args[0]}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	tr := newTest(t, eng)
	got, err := tr.Transcribe(context.Background(), speech(time.Second), rate)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "transcribed in another process" || got.RawText != workerText {
		t.Errorf("Transcribe = %+v", got)
	}
}
