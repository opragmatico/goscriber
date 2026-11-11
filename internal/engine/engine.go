package engine

import (
	"context"
	"time"
)

// Engine recognizes speech with one loaded model at a time.
//
// An Engine is safe for concurrent use: Recognize calls may overlap each
// other and open streams. Load and Unload wait for calls in flight.
type Engine interface {
	// Load loads the model spec describes and reports what it can do. A model
	// that is already loaded is unloaded first, as by Unload.
	Load(ctx context.Context, spec LoadSpec) (ModelInfo, error)

	// Unload closes any stream still open, frees the model and returns its
	// memory. Unloading with no model loaded does nothing.
	Unload() error

	// Recognize decodes a whole clip: mono samples in [-1, 1] at sampleRate
	// Hz. The engine resamples rates from MinSampleRate to MaxSampleRate to
	// the model's own; 0 means the samples are already at the model's rate.
	// The Result is final. Recognize reads samples only until it returns.
	//
	// Cancelling ctx makes Recognize fail with ErrCancelled, but the engine
	// may not be able to stop a decode it has started: it then finishes the
	// decode and discards the result before returning.
	Recognize(ctx context.Context, samples []float32, sampleRate int, opts Options) (Result, error)

	// StartStream opens a stream that decodes audio as it is pushed. Models
	// that cannot stream (ModelInfo.Streaming is false) fail with
	// ErrInvalidArgument: callers fall back to Recognize. ctx bounds only the
	// start; the stream lives until it is closed.
	StartStream(ctx context.Context, opts Options) (Stream, error)
}

// Stream is one streaming recognition: push audio as it arrives, pull interim
// and final results, then finish. Drive a stream from one goroutine at a
// time.
//
// A typical session:
//
//	for audio := range chunks {
//		s.Push(audio, rate)
//		for r, err := s.Next(); r != nil && err == nil; r, err = s.Next() {
//			show(r) // interim, or final
//		}
//	}
//	s.Finish()
//	for r, err := s.Next(); err != io.EOF; r, err = s.Next() { ... } // the end-of-stream final
//	s.Close()
type Stream interface {
	// Push buffers mono samples in [-1, 1] at sampleRate Hz (see
	// Engine.Recognize for the rates allowed). The rate is fixed by the first
	// Push and cannot change within a stream. Push does not decode; Next
	// does. The stream keeps no reference to samples.
	Push(samples []float32, sampleRate int) error

	// Next decodes the audio pushed so far and returns the next result, or
	// nil when there is none yet and the stream needs more audio. After
	// Finish it returns the end-of-stream final and then io.EOF.
	Next() (*Result, error)

	// ForceEndpoint ends the current utterance now: the next result is a
	// final for the audio pushed so far, and the stream carries on.
	ForceEndpoint() error

	// Finish declares the end of the audio and flushes it through the model.
	// Next then returns the end-of-stream final. Push fails after Finish.
	Finish() error

	// Close releases the stream. It is safe to call more than once; any
	// other call on a closed stream fails with ErrInvalidArgument.
	Close() error
}

// Sample rates Recognize and Push accept; the engine resamples them to the
// model's own rate.
const (
	MinSampleRate = 8000
	MaxSampleRate = 96000
)

// LoadSpec says which model to load and how to run it.
type LoadSpec struct {
	// ID names the model, such as a catalog id. ModelInfo echoes it.
	ID string
	// Path is the model's GGUF file. The engine never downloads anything.
	Path string
	// VADPath is an optional Silero VAD GGUF that masks silence before the
	// model sees it. Empty means no VAD.
	VADPath string
	// CPU runs the model on the CPU even when the build has a GPU backend
	// (Metal on macOS).
	CPU bool
	// Latency is how far a streaming model looks ahead. Empty means
	// LatencyBalanced; offline models ignore it.
	Latency Latency
}

// Latency trades how soon a streaming model commits to text against how
// accurate that text is: the model looks further ahead in the audio before
// deciding on each word.
type Latency string

// Latency presets.
const (
	// LatencyLow shows words soonest, at some cost in accuracy.
	LatencyLow Latency = "low"
	// LatencyBalanced is the model's own trade-off.
	LatencyBalanced Latency = "balanced"
	// LatencyAccurate looks furthest ahead: the most accurate text, shown
	// latest.
	LatencyAccurate Latency = "accurate"
)

// ModelInfo is what a loaded model can do.
type ModelInfo struct {
	// ID is the LoadSpec's ID.
	ID string
	// Streaming reports whether the model decodes audio live through
	// StartStream. Offline-only models decode whole clips with Recognize.
	Streaming bool
	// Languages lists the values the model accepts in Options.Language, such
	// as "en-US", including "auto" when it can detect the language. Empty
	// means the model has one fixed language and ignores Options.Language.
	Languages []string
}

// DecodeMode is how a recording is turned into text.
type DecodeMode int

// Decode modes.
const (
	// DecodeOnce records the whole clip, then decodes it with Recognize.
	DecodeOnce DecodeMode = iota
	// DecodeLive decodes while recording through a Stream, so interim text
	// is available and the final arrives soon after recording stops.
	DecodeLive
)

// String returns "once" or "live".
func (m DecodeMode) String() string {
	switch m {
	case DecodeOnce:
		return "once"
	case DecodeLive:
		return "live"
	default:
		return "unknown"
	}
}

// DecodeMode returns how recordings for this model are decoded: streaming
// models decode live, the others once on the whole clip.
func (m ModelInfo) DecodeMode() DecodeMode {
	if m.Streaming {
		return DecodeLive
	}
	return DecodeOnce
}

// Options are per-request recognition options.
type Options struct {
	// Language selects the language, one of ModelInfo.Languages. Empty or
	// "auto" lets the model detect it; single-language models ignore it.
	Language string
	// InterimResults makes a stream return partial hypotheses before each
	// final. Recognize ignores it.
	InterimResults bool
	// WordTimestamps fills Result.Words.
	WordTimestamps bool
	// Phrases are words or phrases to favour, such as names and jargon.
	// Models without word boosting ignore them.
	Phrases []string
	// Boost is how strongly Phrases are favoured: a score added each time
	// one is emitted. Typical values are 2 to 3 for Nemotron models.
	Boost float32
}

// Result is a transcript, interim or final.
type Result struct {
	// Text is the transcript, with the punctuation and capitalization the
	// model writes.
	Text string
	// Final reports whether Text is settled. Interim results are replaced by
	// later ones; finals are not. Recognize always returns a final.
	Final bool
	// Confidence is the model's confidence in Text, from 0 to 1. Interim
	// results and some models report 0 or 1 only.
	Confidence float32
	// Words is Text word by word, when Options.WordTimestamps was set.
	// Interim results may have none.
	Words []Word
	// Languages are the languages the model selected or detected.
	Languages []string
	// AudioProcessed is how much audio the result covers, excluding audio
	// still buffered as lookahead. The end-of-stream final covers it all.
	AudioProcessed time.Duration
}

// Word is one word of a Result with its position in the audio. Positions are
// where the model emitted the word, not a forced alignment.
type Word struct {
	Text       string
	Start      time.Duration
	End        time.Duration
	Confidence float32
}
