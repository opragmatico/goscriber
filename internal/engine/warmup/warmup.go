// Package warmup decodes silence as soon as a model loads, so the first
// dictation does not pay for what a model's first decode sets up.
//
// On Metal the first decode after a load compiles the GPU pipelines and
// allocates the compute buffers: Parakeet TDT's first decode of an 11 s clip
// takes three to five times as long as the next one. An Engine wraps another
// engine.Engine and, after every Load, decodes silence the way dictation will
// use the model: an offline model decodes a clip as long as the longest the
// caller hands it at once, which compiles the pipelines and sizes the buffers
// for every later decode; a streaming model decodes Silence whole and through
// a stream. The cost moves into the load, which the app runs at launch, in
// the background, or while the user speaks.
package warmup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/opragmatico/goscriber/engine"
)

// Silence is the shortest warm-up: enough for the pipelines, and all a
// streaming model gets, whose stream decodes the audio in small steps
// however long the dictation.
const Silence = 500 * time.Millisecond

// sampleRate is the rate of the silence: the models' own, so nothing is
// resampled.
const sampleRate = 16000

// Options configure an Engine.
type Options struct {
	// Longest is the longest clip the caller will hand an offline model at
	// once, such as the longest chunk dictation cuts. Its warm-up decodes
	// that much silence, so the compute buffers fit every later decode: a
	// decode longer than any before it grows them, a few milliseconds on
	// Metal. Values under Silence mean Silence.
	Longest time.Duration
	// Log receives a line per warm-up. Nil means slog.Default().
	Log *slog.Logger
}

// Engine is an engine.Engine that warms each model up as it loads. Every
// other call goes straight to the inner engine. It is safe for concurrent
// use as far as the inner engine is.
type Engine struct {
	engine.Engine
	longest time.Duration
	log     *slog.Logger
}

var _ engine.Engine = (*Engine)(nil)

// New returns an Engine over inner.
func New(inner engine.Engine, opts Options) *Engine {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Engine{Engine: inner, longest: max(opts.Longest, Silence), log: opts.Log}
}

// Load implements engine.Engine: it loads spec, then decodes the silence. A
// warm-up that fails is logged, not returned: the model is loaded, and the
// first dictation sets up what the warm-up could not. Cancelling ctx cuts the
// warm-up short too.
func (e *Engine) Load(ctx context.Context, spec engine.LoadSpec) (engine.ModelInfo, error) {
	info, err := e.Engine.Load(ctx, spec)
	if err != nil {
		return info, err
	}
	start := time.Now()
	if err := Run(ctx, e.Engine, info, e.longest); err != nil {
		if ctx.Err() != nil {
			e.log.Debug("model warm-up cut short", "model", spec.ID, "err", err)
		} else {
			e.log.Warn("warming up the model failed; the first dictation may be slower", "model", spec.ID, "err", err)
		}
		return info, nil
	}
	e.log.Debug("model warmed up", "model", spec.ID, "streaming", info.Streaming, "took", time.Since(start).Round(time.Millisecond))
	return info, nil
}

// Run warms up eng's loaded model, which info describes. An offline model
// decodes longest of silence with Recognize. A streaming model decodes
// Silence with Recognize, its fallback, then streams Silence through a
// stream that is finished and drained.
func Run(ctx context.Context, eng engine.Engine, info engine.ModelInfo, longest time.Duration) error {
	clip := Silence
	if !info.Streaming {
		clip = max(longest, Silence)
	}
	if _, err := eng.Recognize(ctx, silence(clip), sampleRate, engine.Options{}); err != nil {
		return fmt.Errorf("decoding silence: %w", err)
	}
	if !info.Streaming {
		return nil
	}
	if err := stream(ctx, eng, silence(Silence)); err != nil {
		return fmt.Errorf("streaming silence: %w", err)
	}
	return nil
}

func silence(d time.Duration) []float32 {
	return make([]float32, int(d*sampleRate/time.Second))
}

// maxResults bounds how many results the warm-up stream may return before
// it gives up on reaching its end.
const maxResults = 1000

func stream(ctx context.Context, eng engine.Engine, samples []float32) error {
	s, err := eng.StartStream(ctx, engine.Options{})
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Push(samples, sampleRate); err != nil {
		return err
	}
	for range maxResults {
		r, err := s.Next()
		if err != nil {
			return err
		}
		if r == nil {
			break
		}
	}
	if err := s.Finish(); err != nil {
		return err
	}
	for range maxResults {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := s.Next(); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
	}
	return errors.New("the stream did not end")
}
