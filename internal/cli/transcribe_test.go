package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/fake"
)

var fixture = filepath.Join("..", "..", "testdata", "audio", "jfk.wav")

// transcribeSetup is a models directory with every model downloaded, empty
// settings, and a fake engine following script.
type transcribeSetup struct {
	*fakeModels
	eng     *fake.Engine
	env     Env
	created int
}

func newTranscribe(t *testing.T, script fake.Script) *transcribeSetup {
	t.Helper()
	t.Setenv(config.HomeEnv, t.TempDir())
	s := &transcribeSetup{fakeModels: newFakeModels(t), eng: fake.New(script)}
	s.place(t, "asr.q8_0.gguf", "batch.gguf", "vad.gguf")
	s.env = Env{Models: s.m, NewEngine: func() engine.Engine { s.created++; return s.eng }}
	return s
}

func TestTranscribe(t *testing.T) {
	s := newTranscribe(t, fake.Script{Replies: []fake.Reply{fake.Transcript("And so, my fellow Americans.")}})
	code, stdout, stderr := runWith(t, s.env, "transcribe", fixture)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if stdout != "And so, my fellow Americans.\n" {
		t.Errorf("stdout %q", stdout)
	}
	loads := s.eng.Loads()
	if len(loads) != 1 {
		t.Fatalf("%d loads", len(loads))
	}
	want := engine.LoadSpec{
		ID: "asr", Path: filepath.Join(s.dir, "asr.q8_0.gguf"),
		VADPath: filepath.Join(s.dir, "vad.gguf"), VADOnset: 0.5, VADOffset: 0.3,
	}
	if loads[0] != want {
		t.Errorf("load spec %+v, want %+v", loads[0], want)
	}
	recs := s.eng.Recognitions()
	if len(recs) != 1 || recs[0].Samples != 176000 || recs[0].SampleRate != 16000 ||
		recs[0].Options.Language != "auto" || recs[0].Options.WordTimestamps {
		t.Errorf("recognitions %+v", recs)
	}
	if s.eng.Loaded() {
		t.Error("the model is still loaded after the command")
	}
	if n := s.requests.Load(); n != 0 {
		t.Errorf("%d requests", n)
	}
}

func TestTranscribeJSON(t *testing.T) {
	s := newTranscribe(t, fake.Script{Replies: []fake.Reply{{Delay: 20 * time.Millisecond, Result: engine.Result{
		Text: "Ask not.", Final: true, Confidence: 0.9, Languages: []string{"en-US"},
		Words: []engine.Word{{Text: "Ask", Start: 1500 * time.Millisecond, End: 1800 * time.Millisecond, Confidence: 0.8}, {Text: "not.", Start: 2 * time.Second, End: 2250 * time.Millisecond, Confidence: 1}},
	}}}})
	code, stdout, stderr := runWith(t, s.env, "transcribe", "--json", "--cpu", "--model", "batch", fixture)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	var got jsonResult
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("%v: %q", err, stdout)
	}
	if got.File != fixture || got.Model != "batch" || got.Quant != "Q8_0" || got.Backend != "cpu" || got.Streamed ||
		got.Text != "Ask not." || got.Language != "en-US" || got.Confidence != 0.9 || got.Audio != 11 {
		t.Errorf("result %+v", got)
	}
	if len(got.Words) != 2 || got.Words[0] != (jsonWord{Text: "Ask", Start: 1.5, End: 1.8, Confidence: 0.8}) {
		t.Errorf("words %+v", got.Words)
	}
	if got.Decode < 0.02 || got.Finalize != got.Decode || got.RTF <= 0 {
		t.Errorf("durations: decode %v, finalize %v, rtf %v", got.Decode, got.Finalize, got.RTF)
	}
	if spec := s.eng.Loads()[0]; !spec.CPU || spec.ID != "batch" {
		t.Errorf("load spec %+v", spec)
	}
	if !s.eng.Recognitions()[0].Options.WordTimestamps {
		t.Error("--json did not ask for word timestamps")
	}
}

func TestTranscribeManyFiles(t *testing.T) {
	s := newTranscribe(t, fake.Script{Replies: []fake.Reply{fake.Transcript("one"), fake.Transcript("two")}})
	bad := filepath.Join(t.TempDir(), "bad.wav")
	_ = os.WriteFile(bad, []byte("not audio"), 0o600)
	code, stdout, stderr := runWith(t, s.env, "transcribe", fixture, bad, fixture)
	if code != 1 {
		t.Errorf("exit %d, want 1 because one file failed", code)
	}
	if want := fixture + ": one\n" + fixture + ": two\n"; stdout != want {
		t.Errorf("stdout %q, want %q", stdout, want)
	}
	if !strings.Contains(stderr, bad) || !strings.Contains(stderr, "not a supported WAV file") {
		t.Errorf("stderr %q", stderr)
	}
	if len(s.eng.Loads()) != 1 {
		t.Error("the model was loaded more than once")
	}
}

func TestTranscribeStream(t *testing.T) {
	script := fake.Dictation("ask what you can do", 2*time.Second)
	script.Final.Languages = []string{"en-US"}
	s := newTranscribe(t, fake.Script{Model: engine.ModelInfo{Streaming: true}, Streams: []fake.StreamScript{script}})
	code, stdout, stderr := runWith(t, s.env, "transcribe", "--stream", "--language", "en-US", fixture)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if stdout != "ask what you can do\n" {
		t.Errorf("stdout %q", stdout)
	}
	for _, interim := range []string{"… ask\n", "… ask what\n", "… ask what you can do\n"} {
		if !strings.Contains(stderr, interim) {
			t.Errorf("stderr lacks interim %q: %q", interim, stderr)
		}
	}
	streams := s.eng.Streams()
	if len(streams) != 1 {
		t.Fatalf("%d streams", len(streams))
	}
	st := streams[0]
	if st.Audio() != 11*time.Second || st.SampleRate() != 16000 || !st.Finished() || !st.Closed() {
		t.Errorf("stream got %v at %d Hz, finished %t, closed %t", st.Audio(), st.SampleRate(), st.Finished(), st.Closed())
	}
	if o := st.Options(); !o.InterimResults || o.Language != "en-US" {
		t.Errorf("stream options %+v", o)
	}
	if len(s.eng.Recognitions()) != 0 {
		t.Error("--stream also ran an offline decode")
	}
}

func TestTranscribeBench(t *testing.T) {
	s := newTranscribe(t, fake.Script{Model: engine.ModelInfo{Streaming: true}, Streams: []fake.StreamScript{fake.Dictation("hello there", time.Second)}})
	code, stdout, stderr := runWith(t, s.env, "transcribe", "--bench", "--stream", fixture)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, want := range []string{"loaded asr Q8_0", "warm-up decode of jfk.wav", "11.000 s of audio decoded in", "real-time factor", "after the end of the audio"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q: %q", want, stderr)
		}
	}
	if strings.Count(stderr, "… hello there") != 1 {
		t.Errorf("the warm-up printed interims: %q", stderr)
	}
	if stdout != "hello there\n" || len(s.eng.Streams()) != 2 {
		t.Errorf("stdout %q, %d streams (warm-up and run)", stdout, len(s.eng.Streams()))
	}
}

func TestTranscribeRefusals(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"batch model streaming", []string{"--model", "batch", "--stream"}, "batch does not stream"},
		{"VAD as a model", []string{"--model", "vad"}, "vad is not a speech recognition model"},
		{"unknown model", []string{"--model", "whisper"}, `unknown model "whisper"`},
		{"unknown quant", []string{"--quant", "Q2_K"}, `unknown quantisation "Q2_K"`},
		{"not downloaded", []string{"--quant", "F16"}, "asr F16 is not downloaded; run 'goscriber models pull asr --quant F16' first"},
		{"unsupported language", []string{"--language", "fr-FR"}, `asr does not transcribe "fr-FR"; it takes auto or one of en-US, de-DE`},
		{"English-only model", []string{"--model", "batch", "--language", "de"}, "batch transcribes en only"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newTranscribe(t, fake.Script{})
			code, stdout, stderr := runWith(t, s.env, append(append([]string{"transcribe"}, tt.args...), fixture)...)
			if code != 1 || stdout != "" || !strings.Contains(stderr, tt.want) {
				t.Errorf("exit %d, stdout %q, stderr %q; want exit 1 and %q", code, stdout, stderr, tt.want)
			}
			if s.created != 0 || len(s.eng.Loads()) != 0 {
				t.Error("the engine was used")
			}
			if n := s.requests.Load(); n != 0 {
				t.Errorf("%d requests", n)
			}
		})
	}
}

func TestTranscribeNotDownloadedHint(t *testing.T) {
	t.Setenv(config.HomeEnv, t.TempDir())
	f := newFakeModels(t)
	code, _, stderr := runWith(t, Env{Models: f.m, NewEngine: func() engine.Engine { t.Error("engine created"); return nil }}, "transcribe", fixture)
	if code != 1 || !strings.Contains(stderr, "asr Q8_0 is not downloaded; run 'goscriber models pull asr' first") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if n := f.requests.Load(); n != 0 {
		t.Errorf("%d requests: transcribe must never download", n)
	}
}

func TestTranscribeDefaultsFromSettings(t *testing.T) {
	s := newTranscribe(t, fake.Script{})
	paths, _ := config.ResolvePaths()
	settings := config.Defaults()
	settings.Language = "de-DE"
	settings.SelectedModel = "batch"
	settings.VADEnabled = false
	settings.Accelerator = config.AcceleratorCPU
	if err := config.Save(paths.SettingsFile(), settings); err != nil {
		t.Fatal(err)
	}
	// The English-only model ignores the German setting rather than failing.
	if code, _, stderr := runWith(t, s.env, "transcribe", fixture); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	spec := s.eng.Loads()[0]
	if spec.ID != "batch" || spec.VADPath != "" || !spec.CPU {
		t.Errorf("load spec %+v: want the selected model without VAD, on the CPU", spec)
	}
	if code, _, stderr := runWith(t, s.env, "transcribe", "--model", "asr", fixture); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if lang := s.eng.Recognitions()[1].Options.Language; lang != "de-DE" {
		t.Errorf("language %q, want the settings' de-DE", lang)
	}
}

func TestTranscribeWithoutVAD(t *testing.T) {
	s := newTranscribe(t, fake.Script{})
	_ = os.Remove(filepath.Join(s.dir, "vad.gguf"))
	code, _, stderr := runWith(t, s.env, "transcribe", fixture)
	if code != 0 || !strings.Contains(stderr, "without voice activity detection") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if spec := s.eng.Loads()[0]; spec.VADPath != "" {
		t.Errorf("VADPath %q", spec.VADPath)
	}
}

func TestTranscribeEngineErrors(t *testing.T) {
	s := newTranscribe(t, fake.Script{LoadErr: engine.ErrOutOfMemory})
	code, _, stderr := runWith(t, s.env, "transcribe", fixture)
	if code != 1 || !strings.Contains(stderr, "out of memory") {
		t.Errorf("load failure: exit %d, %q", code, stderr)
	}

	s = newTranscribe(t, fake.Script{Replies: []fake.Reply{{Err: engine.ErrRuntime}}})
	code, _, stderr = runWith(t, s.env, "transcribe", fixture)
	if code != 1 || !strings.Contains(stderr, "runtime error") {
		t.Errorf("recognize failure: exit %d, %q", code, stderr)
	}

	// The catalog says the model streams but the engine refuses.
	s = newTranscribe(t, fake.Script{Model: engine.ModelInfo{Streaming: false}})
	code, _, stderr = runWith(t, s.env, "transcribe", "--stream", fixture, fixture)
	if code != 1 || !strings.Contains(stderr, "asr does not stream") || strings.Count(stderr, "goscriber:") != 1 {
		t.Errorf("stream refusal: exit %d, %q", code, stderr)
	}
}

func TestTranscribeUsage(t *testing.T) {
	s := newTranscribe(t, fake.Script{})
	if code, _, stderr := runWith(t, s.env, "transcribe"); code != 2 || !strings.Contains(stderr, "no FILE given") {
		t.Errorf("no file: exit %d, %q", code, stderr)
	}
	if code, stdout, _ := runWith(t, s.env, "transcribe", "--help"); code != 0 || !strings.Contains(stdout, "--stream") {
		t.Errorf("--help: exit %d, %q", code, stdout)
	}
	code, _, stderr := runWith(t, Env{Models: s.m}, "transcribe", fixture)
	if code != 1 || !strings.Contains(stderr, "no speech engine") {
		t.Errorf("no engine: exit %d, %q", code, stderr)
	}
}

func TestJoinFinals(t *testing.T) {
	r := joinFinals([]engine.Result{
		{Text: "one.", Confidence: 1, Languages: []string{"en-US"}, AudioProcessed: time.Second, Words: []engine.Word{{Text: "one."}}},
		{Text: "", Confidence: 0},
		{Text: "two.", Confidence: 0.5, Languages: []string{"en-US", "de-DE"}, AudioProcessed: 2 * time.Second, Words: []engine.Word{{Text: "two."}}},
	})
	if r.Text != "one. two." || len(r.Words) != 2 || r.AudioProcessed != 2*time.Second || len(r.Languages) != 2 || r.Confidence != 0.5 {
		t.Errorf("joined %+v", r)
	}
}

func TestTranscribeVADThresholdsFromSettings(t *testing.T) {
	s := newTranscribe(t, fake.Script{})
	paths, _ := config.ResolvePaths()
	settings := config.Defaults()
	settings.VADOnset, settings.VADOffset = 0.6, 0.45
	if err := config.Save(paths.SettingsFile(), settings); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runWith(t, s.env, "transcribe", fixture); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if spec := s.eng.Loads()[0]; spec.VADPath == "" || spec.VADOnset != 0.6 || spec.VADOffset != 0.45 {
		t.Errorf("load spec %+v: want the VAD with onset 0.6 and offset 0.45", spec)
	}
}
