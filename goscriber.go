// Package goscriber transcribes speech offline with Goscriber's engine, for Go
// programs in which speech-to-text is one step of their own work: a workflow
// node, an agent's tool, a batch job.
//
// A Transcriber turns audio into text the way the Goscriber app does: with the
// model, language and custom words of Goscriber's settings unless Options say
// otherwise, long audio decoded in pieces, and the transcript tidied by the
// app's text cleanup. It uses the models Goscriber has downloaded (goscriber
// models pull) and downloads nothing. A Transcriber is safe for concurrent
// use, and its results and options are plain values with JSON names, so they
// can travel through a workflow's state as they are.
//
// The speech engine is NeMo-Speech.cpp, a C++ library. By default it runs in
// a worker process of an installed goscriber binary, one built with make deps
// build, the way the app runs it: this package then needs no CGo, and a crash
// in the engine ends the worker, which the next transcription starts again,
// rather than the program. A program built with the nemo build tag against
// the NeMo-Speech.cpp SDK can run the engine in its own process instead
// (Options.InProcess).
//
// Package hook rewrites transcripts with Goscriber's transcription hook, or
// any other program, as a step of its own: see Transcript.HookInfo.
package goscriber

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/opragmatico/goscriber/hook"
	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/warmup"
	"github.com/opragmatico/goscriber/internal/engine/worker"
	"github.com/opragmatico/goscriber/internal/models"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/output"
	"github.com/opragmatico/goscriber/internal/pipeline"
)

// Errors a Transcriber returns; test for them with errors.Is. They tell a
// workflow which failures another attempt cannot fix: all of these but
// ErrClosed.
var (
	// ErrNoSpeech: the audio holds no speech, so it has no transcript. It is
	// too short or too quiet, or the model heard no words in it.
	ErrNoSpeech = pipeline.ErrNoSpeech
	// ErrNoModel: the model is not a speech model in Goscriber's catalog, or
	// is not downloaded; goscriber models pull downloads it.
	ErrNoModel = pipeline.ErrNoModel
	// ErrModelCorrupt: the model's file is damaged; goscriber models pull
	// downloads it again.
	ErrModelCorrupt = pipeline.ErrModelCorrupt
	// ErrUnsupportedAudio: the audio is neither WAV nor FLAC, or is at a
	// sample rate the engine does not take.
	ErrUnsupportedAudio = errors.New("unsupported audio")
	// ErrNoEngine: there is no speech engine to run: no goscriber binary for
	// a worker, or InProcess in a program built without one.
	ErrNoEngine = errors.New("no speech engine")
	// ErrClosed: the Transcriber was closed.
	ErrClosed = pipeline.ErrClosed
)

// Options configure a Transcriber. Those left empty fall back to Goscriber's
// settings (settings.json in its config directory, $GOSCRIBER_HOME when it is
// set), and then to Goscriber's defaults, as for goscriber transcribe.
type Options struct {
	// Model is the catalog ID of the model to transcribe with, such as
	// "parakeet-tdt-0.6b-v2" or "nemotron-3.5-asr-streaming-0.6b". Empty
	// means the model selected in the settings, else the catalog's default.
	Model string
	// Language is the language to transcribe: "auto" to detect it, or one
	// of the locales the model takes, such as "pt-BR". Empty means the
	// settings' language. A model with a language of its own ignores it.
	Language string
	// Words are names and jargon to spell as written, on top of the
	// settings' custom words. A model that can favour words is asked to;
	// another's transcripts are corrected.
	Words []string
	// Executable is the goscriber binary whose engine worker runs the
	// speech engine; its version must speak the same worker protocol as
	// this package's. Empty means goscriber in PATH.
	Executable string
	// InProcess runs the speech engine in this process instead of a worker,
	// which only a program built with the nemo build tag can do.
	InProcess bool
	// Log receives a line per transcription and the engine's log. Nil means
	// slog.Default().
	Log *slog.Logger
}

// Transcript is the text of some audio.
type Transcript struct {
	// Text is the transcript after Goscriber's text cleanup: custom words
	// spelled as given, filler words removed (unless the settings say
	// otherwise) and spacing tidied.
	Text string `json:"text"`
	// RawText is the model's transcript, before the cleanup.
	RawText string `json:"raw_text"`
	// Model is the catalog ID of the model that transcribed the audio.
	Model string `json:"model"`
	// Language is the transcript's language as best known: the one the
	// model detected, else the one it was asked for, else the model's only
	// language, such as "es-US" or "en". It is "" when not known.
	Language string `json:"language,omitempty"`
	// Duration is how long the audio lasts; in JSON, in nanoseconds.
	Duration time.Duration `json:"duration_ns"`
}

// HookInfo is what a hook is told about the transcript, for rewriting it as
// Goscriber rewrites a dictation before pasting it:
//
//	text, err := h.Rewrite(ctx, tr.Text, tr.HookInfo())
func (t Transcript) HookInfo() hook.Info { return hook.Info{Model: t.Model, Language: t.Language} }

// Option tunes one transcription.
type Option func(*transcribeOptions)

type transcribeOptions struct {
	progress func(done, total time.Duration)
}

// OnProgress has fn told how much of the audio has been decoded, out of its
// whole length, after every ten seconds of it and last with done equal to
// total. It is called on the goroutine transcribing. A workflow node can pass
// it on as a heartbeat.
func OnProgress(fn func(done, total time.Duration)) Option {
	return func(o *transcribeOptions) { o.progress = fn }
}

// Transcriber transcribes audio with one model. The first transcription loads
// the model into the engine and warms it up, which takes from a fraction of a
// second to a few, and the model stays loaded until Close. Create one per program and share it: transcriptions run
// one at a time on the engine, each a slice of audio at a time, so a short one
// started during a long one does not wait for all of it.
type Transcriber struct {
	c   *pipeline.Coordinator
	eng engine.Engine
}

// New returns a Transcriber for the model that the options and Goscriber's
// settings select. It fails with ErrNoModel or ErrModelCorrupt when that
// model is not ready, and with ErrNoEngine when there is no engine to run it
// on. Close it when done.
func New(o Options) (*Transcriber, error) {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		return nil, fmt.Errorf("goscriber: %w", err)
	}
	settings, _, err := config.Load(paths.SettingsFile())
	if err != nil {
		return nil, fmt.Errorf("goscriber: %w", err)
	}
	files := models.NewManager(paths.ModelsDir(), models.Options{HFCache: models.HFCacheDir(os.Getenv)})
	if settings, err = apply(o, settings, files.Catalog()); err != nil {
		return nil, err
	}
	src := pipeline.CatalogModels{Manager: files}
	if _, err := src.Resolve(settings); err != nil {
		return nil, fmt.Errorf("goscriber: %w; download it with 'goscriber models pull %s'", err, settings.SelectedModel)
	}
	eng, err := newEngine(o, log)
	if err != nil {
		return nil, err
	}
	// As in the app, the engine decodes silence right after loading the
	// model, so the first transcription does not run cold on the GPU.
	eng = warmup.New(eng, warmup.Options{Longest: pipeline.LongestChunk, Log: log})
	return newTranscriber(eng, src, settings, log), nil
}

// apply puts the options over the settings, after checking them against the
// catalog.
func apply(o Options, s config.Settings, cat *models.Catalog) (config.Settings, error) {
	s = s.Clone()
	id := cmp.Or(o.Model, s.SelectedModel, cat.DefaultModel)
	m, ok := cat.Model(id)
	if !ok || m.Kind != models.KindASR {
		return s, fmt.Errorf("goscriber: %w: %q is not a speech model in the catalog", ErrNoModel, id)
	}
	s.SelectedModel = id
	if o.Language != "" {
		choices := m.LanguageChoices()
		if choices != nil && !slices.ContainsFunc(choices, func(c string) bool { return strings.EqualFold(c, o.Language) }) {
			return s, fmt.Errorf("goscriber: %s does not transcribe %q; it takes %s", id, o.Language, strings.Join(choices, ", "))
		}
		s.Language = o.Language
	}
	s.CustomWords = append(s.CustomWords, o.Words...)
	return s, nil
}

// inProcessEngine is the engine built into this program, which engine_nemo.go
// sets in builds with the nemo tag; nil otherwise.
var inProcessEngine func() engine.Engine

// newEngine is the engine the options ask for: a supervised worker process
// of a goscriber binary, or the engine built into this program.
func newEngine(o Options, log *slog.Logger) (engine.Engine, error) {
	if o.InProcess {
		if inProcessEngine == nil {
			return nil, fmt.Errorf("goscriber: %w in this program: build it with -tags nemo against the NeMo-Speech.cpp SDK, or leave InProcess off to run the engine in a goscriber worker", ErrNoEngine)
		}
		return inProcessEngine(), nil
	}
	exe := o.Executable
	if exe == "" {
		p, err := exec.LookPath("goscriber")
		if err != nil {
			return nil, fmt.Errorf("goscriber: %w: no goscriber binary in PATH; set Options.Executable to one built with 'make deps build'", ErrNoEngine)
		}
		exe = p
	}
	// Workers start whenever the engine needs one, maybe after this program
	// has changed directory.
	exe, err := filepath.Abs(exe)
	if err != nil {
		return nil, fmt.Errorf("goscriber: %w: %w", ErrNoEngine, err)
	}
	fi, err := os.Stat(exe)
	switch {
	case err != nil:
		return nil, fmt.Errorf("goscriber: %w: %w", ErrNoEngine, err)
	case !fi.Mode().IsRegular() || runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0:
		return nil, fmt.Errorf("goscriber: %w: %s is not an executable file", ErrNoEngine, exe)
	}
	return worker.New(worker.Options{
		Command: func() (*exec.Cmd, error) { return exec.Command(exe, worker.Command), nil },
		Log:     log,
	}), nil
}

// newTranscriber runs the dictation coordinator for its transcriptions alone:
// with no microphone and nothing to paste into.
func newTranscriber(eng engine.Engine, src pipeline.ModelSource, settings config.Settings, log *slog.Logger) *Transcriber {
	c := pipeline.New(pipeline.Options{
		Recorder: noMicrophone{},
		Engine:   eng,
		Models:   src,
		Output:   noOutput{},
		Notices:  noNotices{},
		Settings: func() config.Settings { return settings },
		Stages:   pipeline.Stages{Cleanup: pipeline.CleanupStage{}},
		Log:      log,
	})
	return &Transcriber{c: c, eng: eng}
}

// Transcribe transcribes audio: mono samples in [-1, 1] at rate Hz, from
// 8 to 96 kHz, which the engine resamples. Audio of any length is decoded the
// way the app decodes a long file: through a stream for a streaming model,
// in chunks cut at pauses for another. Cancelling ctx stops it.
func (t *Transcriber) Transcribe(ctx context.Context, samples []float32, rate int, opts ...Option) (Transcript, error) {
	if rate < engine.MinSampleRate || rate > engine.MaxSampleRate {
		return Transcript{}, fmt.Errorf("goscriber: %w: %d Hz is outside the %d to %d Hz the engine takes",
			ErrUnsupportedAudio, rate, engine.MinSampleRate, engine.MaxSampleRate)
	}
	var o transcribeOptions
	for _, opt := range opts {
		opt(&o)
	}
	res, err := t.c.Transcribe(ctx, samples, rate, pipeline.TranscribeOptions{Progress: o.progress})
	if err != nil {
		return Transcript{}, fmt.Errorf("goscriber: %w", err)
	}
	return Transcript{
		Text:     res.Text,
		RawText:  res.RawText,
		Model:    res.Model,
		Language: res.Language,
		Duration: time.Duration(len(samples)) * time.Second / time.Duration(rate),
	}, nil
}

// TranscribeFile transcribes an audio file, WAV or FLAC at any sample rate,
// told apart by its content and mixed down to mono; see Transcribe.
func (t *Transcriber) TranscribeFile(ctx context.Context, path string, opts ...Option) (Transcript, error) {
	samples, rate, err := audio.ReadFile(path)
	if err != nil {
		return Transcript{}, audioError(err)
	}
	return t.Transcribe(ctx, samples, rate, opts...)
}

// TranscribeReader transcribes audio read from r, a WAV or FLAC stream, such
// as an upload or an object from storage; see TranscribeFile.
func (t *Transcriber) TranscribeReader(ctx context.Context, r io.Reader, opts ...Option) (Transcript, error) {
	samples, rate, err := audio.Read(r)
	if err != nil {
		return Transcript{}, audioError(err)
	}
	return t.Transcribe(ctx, samples, rate, opts...)
}

// audioError is a failure to read audio, matching ErrUnsupportedAudio when
// the audio is in a format Goscriber does not decode.
func audioError(err error) error {
	if errors.Is(err, audio.ErrUnsupportedFile) || errors.Is(err, audio.ErrMP3) || errors.Is(err, audio.ErrNotWAV) {
		return fmt.Errorf("goscriber: %w: %w", ErrUnsupportedAudio, err)
	}
	return fmt.Errorf("goscriber: %w", err)
}

// Close lets transcriptions already on the engine finish, fails those still
// to start with ErrClosed, and unloads the model, which ends the engine's
// worker process.
func (t *Transcriber) Close() error {
	t.c.Close()
	return t.eng.Unload()
}

// noMicrophone is the recorder of a Transcriber, which never records.
type noMicrophone struct{}

func (noMicrophone) Start() error               { return errors.New("goscriber: a Transcriber does not record") }
func (noMicrophone) Stop() []float32            { return nil }
func (noMicrophone) SampleRate() int            { return 0 }
func (noMicrophone) Frames() <-chan []float32   { return nil }
func (noMicrophone) Levels() <-chan audio.Level { return nil }

// noOutput is the output of a Transcriber, which never pastes.
type noOutput struct{}

func (noOutput) Deliver(context.Context, string, output.Options) error {
	return errors.New("goscriber: a Transcriber does not paste")
}

// noNotices drops notices: a Transcriber reports through its errors.
type noNotices struct{}

func (noNotices) Publish(notice.Notice) bool { return false }
func (noNotices) Resolve(notice.Code) bool   { return false }
