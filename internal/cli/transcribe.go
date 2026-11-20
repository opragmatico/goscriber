package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/models"
	"github.com/opragmatico/goscriber/internal/text"
)

const transcribeUsage = `Usage:
  goscriber transcribe [flags] FILE...

Transcribes WAV (PCM or float) or FLAC files, at any sample rate, mixed down
to mono, and prints each transcript on stdout, prefixed with its file name
when there is more than one file.

Flags:
      --model ID      model to use (default: the model selected in settings,
                      else nemotron-3.5-asr-streaming-0.6b)
      --quant Q       quantisation (default: the settings' one if the model has
                      it, else the model's own, Q8_0)
      --language L    language, such as en-US, or auto to detect it (default:
                      the settings' one, auto); English-only models ignore it
      --stream        decode through the streaming path, in 20 ms chunks, and
                      print interim text to stderr (streaming models only)
      --cpu           run on the CPU instead of the GPU (Metal on macOS); the
                      default when the accelerator setting is "cpu"
      --json          print one JSON object per file: text, words with their
                      timings, language and durations
      --bench         print load time, real-time factor and finalisation
                      latency to stderr, after an untimed warm-up decode
      --word WORD     a custom word (a name, jargon) to spell this way, on top
                      of the settings' custom_words; repeat for more
      --llm           clean each transcript with the language model set up
                      in the settings' Post-processing page, on or off there
      --llm-model M   the model to clean with; implies --llm (default: the
                      settings' llm_model, else the provider's default:
                      claude-opus-5-5 for Claude, and for a provider on
                      this computer gemma4:26b with 32 GB of memory or more,
                      gemma4:e4b with less)
      --llm-url URL   the provider's base URL, which picks the provider at
                      that address, else a custom OpenAI-compatible server;
                      implies --llm (default: the settings' provider)

Custom words are boosted by models that can favour words (Nemotron), with
the settings' custom_word_boost. Other models' transcripts are corrected:
near-misses of the words, by word_correction_threshold, take their spelling.
With --json, "text" is corrected and "words" are as the model wrote them.

A <unk> the speech model writes for a character it has no token for
becomes "%" after a number and ":" elsewhere.

With --llm, the transcript goes to the model with the settings' prompt, in
parts of up to 400 words cut at sentence ends; the prompt's ${custom_words}
are the settings' custom words and --word's. A part the model fails on is
printed as transcribed, with a warning, and the command exits 1. The API key
is the one the settings window stored in the system keychain for the
provider. With --json, "text" is the cleaned transcript and "raw_text" the
speech model's.

Nothing is downloaded: fetch the model first with 'goscriber models pull'.
The voice activity detector is used when it is downloaded and enabled in
settings, with the settings' onset and offset. NeMo-Speech.cpp applies it to
streamed audio (--stream) only.
`

// streamChunk is how much audio --stream pushes at a time, like a microphone
// callback.
const streamChunk = 20 * time.Millisecond

type transcribeFlags struct {
	model, quant, language string
	languageSet            bool
	stream, cpu, json      bool
	bench                  bool
	words                  wordList
	llm                    bool
	llmModel, llmURL       string
}

// wordList collects repeated --word flags.
type wordList []string

func (w *wordList) String() string { return strings.Join(*w, ",") }

func (w *wordList) Set(v string) error {
	*w = append(*w, v)
	return nil
}

func runTranscribe(args []string, env Env) int {
	c := newCommand("transcribe", transcribeUsage, env)
	var fl transcribeFlags
	c.fs.StringVar(&fl.model, "model", "", "model id")
	c.fs.StringVar(&fl.quant, "quant", "", "quantisation")
	c.fs.StringVar(&fl.language, "language", "", "language or auto")
	c.fs.BoolVar(&fl.stream, "stream", false, "stream")
	c.fs.BoolVar(&fl.cpu, "cpu", false, "run on the CPU")
	c.fs.BoolVar(&fl.json, "json", false, "JSON output")
	c.fs.BoolVar(&fl.bench, "bench", false, "benchmark")
	c.fs.Var(&fl.words, "word", "custom word")
	c.fs.BoolVar(&fl.llm, "llm", false, "post-process with a language model")
	c.fs.StringVar(&fl.llmModel, "llm-model", "", "language model")
	c.fs.StringVar(&fl.llmURL, "llm-url", "", "language model base URL")
	files, code, ok := c.parse(args)
	if !ok {
		return code
	}
	if len(files) == 0 {
		return c.fail("no FILE given")
	}
	c.fs.Visit(func(f *flag.Flag) {
		if f.Name == "language" {
			fl.languageSet = true
		}
	})

	t, err := newTranscriber(env, fl)
	if err != nil {
		fmt.Fprintf(env.Stderr, "goscriber: %v\n", err)
		return exitFailure
	}
	ctx, stop := interruptible()
	defer stop()
	if t.post != nil {
		if err := t.post.check(ctx); err != nil {
			return t.exit(ctx, err)
		}
	}
	if err := t.load(ctx); err != nil {
		return t.exit(ctx, err)
	}
	defer func() { _ = t.eng.Unload() }()

	if fl.bench {
		if err := t.warmUp(ctx, files[0]); err != nil {
			return t.exit(ctx, err)
		}
	}
	failed := false
	for _, file := range files {
		if err := t.transcribe(ctx, file, len(files) > 1); err != nil {
			if ctx.Err() != nil || errors.Is(err, errNoStream) {
				return t.exit(ctx, err)
			}
			fmt.Fprintf(env.Stderr, "goscriber: %s: %v\n", file, err)
			failed = true
		}
	}
	if failed {
		return exitFailure
	}
	return exitOK
}

// transcriber is one run of the transcribe command: a model and the flags.
type transcriber struct {
	env   Env
	fl    transcribeFlags
	model *models.Model
	file  models.File
	path  string // the model's GGUF
	vad   string // the VAD's GGUF, or "" to run without
	opts  engine.Options
	// The VAD's speech thresholds, from the settings.
	vadOnset, vadOffset float32
	// correct are the custom words to correct transcripts with, when the
	// model does not boost them, by threshold.
	correct   []string
	threshold float64
	// post cleans the transcripts with a language model, with --llm.
	post *postProcessor

	eng      engine.Engine
	info     engine.ModelInfo
	loadTime time.Duration
}

// newTranscriber resolves the model, quantisation, language and files from
// the flags and settings, without loading anything. Every check that does
// not need the engine happens here, so mistakes fail fast.
func newTranscriber(env Env, fl transcribeFlags) (*transcriber, error) {
	settings := config.Defaults()
	if paths, err := config.ResolvePaths(); err == nil {
		if s, _, err := config.Load(paths.SettingsFile()); err == nil {
			settings = s
		}
	}
	m, err := env.modelManager()
	if err != nil {
		return nil, err
	}
	cat := m.Catalog()

	id := firstNonEmpty(fl.model, settings.SelectedModel, cat.DefaultModel)
	model, ok := cat.Model(id)
	if !ok {
		_, err := m.Status(id, "")
		return nil, err
	}
	if model.Kind != models.KindASR {
		return nil, fmt.Errorf("%s is not a speech recognition model", id)
	}
	quant := fl.quant
	if quant == "" {
		if _, ok := model.File(settings.ModelQuant); ok {
			quant = settings.ModelQuant
		}
	}
	f, ok := model.File(quant)
	if !ok {
		_, err := m.Status(id, quant)
		return nil, err
	}
	if fl.stream && !model.Capabilities.Streaming {
		return nil, fmt.Errorf("%s does not stream: it transcribes whole recordings; drop --stream", id)
	}

	if settings.Accelerator == config.AcceleratorCPU {
		fl.cpu = true
	}
	lang := settings.Language
	if fl.languageSet {
		lang = fl.language
	}
	if err := checkLanguage(model, lang, fl.languageSet); err != nil {
		return nil, err
	}

	path, err := m.Path(id, f.Quant)
	switch {
	case errors.Is(err, models.ErrNotDownloaded):
		pull := "goscriber models pull " + id
		if !strings.EqualFold(f.Quant, model.DefaultQuant) {
			pull += " --quant " + f.Quant
		}
		return nil, fmt.Errorf("%s %s is not downloaded; run '%s' first", id, f.Quant, pull)
	case errors.Is(err, models.ErrCorrupt):
		return nil, fmt.Errorf("%v; run 'goscriber models pull %s' to replace it", err, id)
	case err != nil:
		return nil, err
	}

	t := &transcriber{
		env:   env,
		fl:    fl,
		model: model,
		file:  f,
		path:  path,
		opts:  engine.Options{Language: lang, WordTimestamps: fl.json},
	}
	words := text.Vocabulary(append(slices.Clone(settings.CustomWords), fl.words...))
	switch {
	case len(words) == 0:
	case model.Capabilities.WordBoosting && settings.CustomWordBoost > 0:
		t.opts.Phrases, t.opts.Boost = words, float32(settings.CustomWordBoost)
	default:
		t.correct, t.threshold = words, settings.WordCorrectionThreshold
	}
	if vad := cat.VAD(); settings.VADEnabled && vad != nil {
		if p, err := m.Path(vad.ID, ""); err == nil {
			t.vad = p
			t.vadOnset, t.vadOffset = float32(settings.VADOnset), float32(settings.VADOffset)
		} else {
			fmt.Fprintf(env.Stderr, "goscriber: transcribing without voice activity detection: %v\n", err)
		}
	}
	if fl.llm || fl.llmModel != "" || fl.llmURL != "" {
		if t.post, err = newPostProcessor(env, settings, fl); err != nil {
			return nil, err
		}
	}
	if env.NewEngine == nil {
		return nil, errors.New("this build has no speech engine; build it with 'make deps build'")
	}
	t.eng = env.NewEngine()
	return t, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// checkLanguage accepts "auto", one of the model's locales, or a bare
// language code one of them starts with ("de" for "de-DE"). A model with one
// language ignores the setting, but an explicit --language for another
// language is a mistake worth reporting.
func checkLanguage(m *models.Model, lang string, explicit bool) error {
	if lang == "" || strings.EqualFold(lang, "auto") {
		return nil
	}
	single := !m.Capabilities.LanguageDetection && len(m.Languages) <= 1
	if single && !explicit {
		return nil
	}
	want := strings.ToLower(lang)
	for _, l := range m.Languages {
		l = strings.ToLower(l)
		if l == want || strings.HasPrefix(l, want+"-") || strings.HasPrefix(want, l+"-") {
			return nil
		}
	}
	if single {
		return fmt.Errorf("%s transcribes %s only; drop --language", m.ID, strings.Join(m.Languages, ""))
	}
	return fmt.Errorf("%s does not transcribe %q; it takes auto or one of %s", m.ID, lang, strings.Join(m.Languages, ", "))
}

// backend names where the model runs: "cpu", or the GPU backend, which is
// Metal on macOS.
func (t *transcriber) backend() string {
	switch {
	case t.fl.cpu:
		return "cpu"
	case runtime.GOOS == "darwin":
		return "metal"
	default:
		return "gpu"
	}
}

func (t *transcriber) load(ctx context.Context) error {
	start := time.Now()
	info, err := t.eng.Load(ctx, engine.LoadSpec{
		ID:        t.model.ID,
		Path:      t.path,
		VADPath:   t.vad,
		VADOnset:  t.vadOnset,
		VADOffset: t.vadOffset,
		CPU:       t.fl.cpu,
	})
	if err != nil {
		return err
	}
	t.info, t.loadTime = info, time.Since(start)
	if t.fl.bench {
		fmt.Fprintf(t.env.Stderr, "loaded %s %s (%s) in %s\n", t.model.ID, t.file.Quant, t.backend(), seconds(t.loadTime))
	}
	return nil
}

// errNoStream: the engine refused to stream the model, which no other file
// will change.
var errNoStream = errors.New("does not stream")

// exit reports a fatal error and returns the exit code for it.
func (t *transcriber) exit(ctx context.Context, err error) int {
	if ctx.Err() != nil || errors.Is(err, engine.ErrCancelled) {
		fmt.Fprintln(t.env.Stderr, "goscriber: interrupted")
		return exitInterrupted
	}
	fmt.Fprintf(t.env.Stderr, "goscriber: %v\n", err)
	return exitFailure
}

// warmUp decodes the first file once, untimed and the same way as the real
// run, so the benchmark measures a warm engine: the first decode also
// prepares the GPU pipelines.
func (t *transcriber) warmUp(ctx context.Context, file string) error {
	samples, rate, err := audio.ReadFile(file)
	if err != nil {
		return err
	}
	start := time.Now()
	if _, _, err := t.decode(ctx, samples, rate, true); err != nil {
		return err
	}
	fmt.Fprintf(t.env.Stderr, "warm-up decode of %s took %s\n", filepath.Base(file), seconds(time.Since(start)))
	return nil
}

// timing is how long a decode took.
type timing struct {
	decode   time.Duration // from the first sample to the final transcript
	finalize time.Duration // from the end of the audio to the final transcript
}

func (t *transcriber) transcribe(ctx context.Context, file string, many bool) error {
	samples, rate, err := audio.ReadFile(file)
	if err != nil {
		return err
	}
	if rate < engine.MinSampleRate || rate > engine.MaxSampleRate {
		return fmt.Errorf("%d Hz is outside the %d to %d Hz the engine takes", rate, engine.MinSampleRate, engine.MaxSampleRate)
	}
	res, tm, err := t.decode(ctx, samples, rate, false)
	if err != nil {
		return err
	}
	res.Text = text.CorrectWords(res.Text, t.correct, t.threshold)
	lang := t.opts.Language
	if len(res.Languages) > 0 {
		lang = res.Languages[0]
	}
	res.Text = text.ReplaceUnknown(res.Text, lang)
	dur := time.Duration(float64(len(samples)) / float64(rate) * float64(time.Second))

	if t.fl.bench {
		rtf := tm.decode.Seconds() / dur.Seconds()
		fmt.Fprintf(t.env.Stderr, "%s: %s of audio decoded in %s: real-time factor %.4f (%.0fx real time); final transcript %s after the end of the audio\n",
			filepath.Base(file), seconds(dur), seconds(tm.decode), rtf, 1/rtf, millis(tm.finalize))
	}
	raw := res.Text
	var postErr error
	if t.post != nil && strings.TrimSpace(res.Text) != "" {
		cleaned, took, err := t.post.clean(ctx, res.Text, lang)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		res.Text, postErr = cleaned, err
		if t.fl.bench {
			fmt.Fprintf(t.env.Stderr, "%s: post-processed with %s in %s\n", filepath.Base(file), t.post.job.Model, seconds(took))
		}
	}
	if t.fl.json {
		if err := t.printJSON(file, res, raw, dur, tm); err != nil {
			return err
		}
		return postErr
	}
	if many {
		fmt.Fprintf(t.env.Stdout, "%s: %s\n", file, res.Text)
	} else {
		fmt.Fprintln(t.env.Stdout, res.Text)
	}
	return postErr
}

// decode runs the samples through the engine, whole or, with --stream, as a
// stream. quiet keeps interim text off stderr.
func (t *transcriber) decode(ctx context.Context, samples []float32, rate int, quiet bool) (engine.Result, timing, error) {
	if !t.fl.stream {
		start := time.Now()
		res, err := t.eng.Recognize(ctx, samples, rate, t.opts)
		d := time.Since(start)
		return res, timing{decode: d, finalize: d}, err
	}
	return t.streamDecode(ctx, samples, rate, quiet)
}

// streamDecode pushes the samples in streamChunk pieces as fast as the
// engine takes them, printing interim text to stderr unless quiet, and
// returns the finals joined into one result.
func (t *transcriber) streamDecode(ctx context.Context, samples []float32, rate int, quiet bool) (engine.Result, timing, error) {
	opts := t.opts
	opts.InterimResults = true
	start := time.Now()
	s, err := t.eng.StartStream(ctx, opts)
	if errors.Is(err, engine.ErrInvalidArgument) {
		return engine.Result{}, timing{}, fmt.Errorf("%s %w (%v)", t.model.ID, errNoStream, err)
	}
	if err != nil {
		return engine.Result{}, timing{}, err
	}
	defer func() { _ = s.Close() }()

	var finals []engine.Result
	lastInterim := ""
	drain := func(untilEOF bool) error {
		for {
			r, err := s.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if r == nil {
				if untilEOF {
					continue
				}
				return nil
			}
			if r.Final {
				finals = append(finals, *r)
				lastInterim = ""
			} else if r.Text != lastInterim && r.Text != "" {
				if !quiet {
					fmt.Fprintf(t.env.Stderr, "… %s\n", r.Text)
				}
				lastInterim = r.Text
			}
		}
	}

	chunk := max(1, int(int64(rate)*int64(streamChunk)/int64(time.Second)))
	for off := 0; off < len(samples); off += chunk {
		if err := ctx.Err(); err != nil {
			return engine.Result{}, timing{}, err
		}
		if err := s.Push(samples[off:min(off+chunk, len(samples))], rate); err != nil {
			return engine.Result{}, timing{}, err
		}
		if err := drain(false); err != nil {
			return engine.Result{}, timing{}, err
		}
	}
	end := time.Now()
	if err := s.Finish(); err != nil {
		return engine.Result{}, timing{}, err
	}
	if err := drain(true); err != nil {
		return engine.Result{}, timing{}, err
	}
	done := time.Now()
	return engine.JoinFinals(finals), timing{decode: done.Sub(start), finalize: done.Sub(end)}, nil
}

type jsonWord struct {
	Text       string  `json:"text"`
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	Confidence float32 `json:"confidence"`
}

type jsonResult struct {
	File       string     `json:"file"`
	Model      string     `json:"model"`
	Quant      string     `json:"quant"`
	Backend    string     `json:"backend"`
	Streamed   bool       `json:"streamed"`
	Text       string     `json:"text"`
	RawText    string     `json:"raw_text,omitempty"`  // with --llm
	LLMModel   string     `json:"llm_model,omitempty"` // with --llm
	Language   string     `json:"language,omitempty"`
	Confidence float32    `json:"confidence"`
	Words      []jsonWord `json:"words"`
	// Durations, in seconds.
	Audio    float64 `json:"audio_duration"`
	Load     float64 `json:"load_time"`
	Decode   float64 `json:"decode_time"`
	Finalize float64 `json:"finalize_latency"`
	RTF      float64 `json:"real_time_factor"`
}

func (t *transcriber) printJSON(file string, res engine.Result, raw string, dur time.Duration, tm timing) error {
	out := jsonResult{
		File:       file,
		Model:      t.model.ID,
		Quant:      t.file.Quant,
		Backend:    t.backend(),
		Streamed:   t.fl.stream,
		Text:       res.Text,
		Confidence: res.Confidence,
		Words:      []jsonWord{},
		Audio:      round3(dur.Seconds()),
		Load:       round3(t.loadTime.Seconds()),
		Decode:     round3(tm.decode.Seconds()),
		Finalize:   round3(tm.finalize.Seconds()),
		RTF:        math.Round(tm.decode.Seconds()/dur.Seconds()*1e5) / 1e5,
	}
	if t.post != nil {
		out.RawText, out.LLMModel = raw, t.post.job.Model
	}
	if len(res.Languages) > 0 {
		out.Language = res.Languages[0]
	}
	for _, w := range res.Words {
		out.Words = append(out.Words, jsonWord{Text: w.Text, Start: round3(w.Start.Seconds()), End: round3(w.End.Seconds()), Confidence: w.Confidence})
	}
	enc := json.NewEncoder(t.env.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func seconds(d time.Duration) string { return fmt.Sprintf("%.3f s", d.Seconds()) }

func millis(d time.Duration) string { return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/1000) }
