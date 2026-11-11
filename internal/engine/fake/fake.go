package fake

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/opragmatico/goscriber/internal/engine"
)

// SampleRate is the model's own rate, which calls passing a sample rate of 0
// get.
const SampleRate = 16000

// Script is what a fake Engine does.
type Script struct {
	// Model is what Load reports. An empty ID takes the LoadSpec's.
	Model engine.ModelInfo
	// LoadDelay is how long Load takes; cancelling its context cuts it short.
	LoadDelay time.Duration
	// LoadErr makes every Load fail. No model is loaded afterwards.
	LoadErr error
	// Replies answer Recognize calls in order, and the last one repeats.
	// Without replies, Recognize returns an empty transcript.
	Replies []Reply
	// Streams script StartStream calls in order, and the last one repeats.
	// Without scripts, streams produce nothing but an empty final.
	Streams []StreamScript
}

// Reply scripts one Recognize call.
type Reply struct {
	// Result is the transcript. Recognize marks it final and, when
	// AudioProcessed is 0, sets that to the clip's duration.
	Result engine.Result
	// Err makes the call fail instead.
	Err error
	// Delay is how long the call takes; cancelling its context cuts it short.
	Delay time.Duration
}

// Transcript is a Reply with text as the transcript.
func Transcript(text string) Reply {
	return Reply{Result: engine.Result{Text: text, Final: true, Confidence: 1}}
}

// Recognition is a Recognize call the engine received.
type Recognition struct {
	Samples    int // number of samples
	SampleRate int // as passed, 0 included
	Options    engine.Options
}

// Engine is a scripted engine.Engine. It is safe for concurrent use.
//
// Scripted errors come back as the Kind of an *engine.Error for the
// operation, unless they already are an *engine.Error.
type Engine struct {
	script Script

	// life is held for reading by every call that needs the model and for
	// writing by Load and Unload, so those wait for calls in flight.
	life   sync.RWMutex
	loaded bool
	model  engine.ModelInfo

	mu           sync.Mutex // guards what follows
	loads        []engine.LoadSpec
	unloads      int
	recognitions []Recognition
	streams      []*Stream
	open         map[*Stream]struct{}
	nextReply    int
	nextStream   int
}

var _ engine.Engine = (*Engine)(nil)

// New returns an Engine that follows s, with no model loaded.
func New(s Script) *Engine {
	return &Engine{script: s, open: map[*Stream]struct{}{}}
}

// Load implements engine.Engine.
func (e *Engine) Load(ctx context.Context, spec engine.LoadSpec) (engine.ModelInfo, error) {
	e.life.Lock()
	defer e.life.Unlock()

	e.mu.Lock()
	e.loads = append(e.loads, spec)
	e.mu.Unlock()
	e.unloadLocked()

	if err := wait(ctx, e.script.LoadDelay); err != nil {
		return engine.ModelInfo{}, cancelled("load", err)
	}
	if e.script.LoadErr != nil {
		return engine.ModelInfo{}, wrap("load", e.script.LoadErr)
	}
	info := e.script.Model
	if info.ID == "" {
		info.ID = spec.ID
	}
	info.Languages = slices.Clone(info.Languages)
	e.loaded, e.model = true, info
	return info, nil
}

// Unload implements engine.Engine.
func (e *Engine) Unload() error {
	e.life.Lock()
	defer e.life.Unlock()
	e.unloadLocked()
	return nil
}

// unloadLocked closes the open streams and drops the model. e.life must be
// held for writing.
func (e *Engine) unloadLocked() {
	if !e.loaded {
		return
	}
	e.mu.Lock()
	for s := range e.open {
		s.markClosed()
	}
	clear(e.open)
	e.unloads++
	e.mu.Unlock()
	e.loaded, e.model = false, engine.ModelInfo{}
}

// Recognize implements engine.Engine.
func (e *Engine) Recognize(ctx context.Context, samples []float32, sampleRate int, opts engine.Options) (engine.Result, error) {
	const op = "recognize"
	e.life.RLock()
	defer e.life.RUnlock()

	if err := ctx.Err(); err != nil {
		return engine.Result{}, cancelled(op, err)
	}
	if !e.loaded {
		return engine.Result{}, &engine.Error{Op: op, Kind: engine.ErrNotLoaded}
	}
	if len(samples) == 0 {
		return engine.Result{}, invalid(op, "empty audio")
	}
	rate, err := checkRate(op, sampleRate)
	if err != nil {
		return engine.Result{}, err
	}

	e.mu.Lock()
	e.recognitions = append(e.recognitions, Recognition{Samples: len(samples), SampleRate: sampleRate, Options: cloneOptions(opts)})
	reply := pick(e.script.Replies, &e.nextReply)
	e.mu.Unlock()

	if err := wait(ctx, reply.Delay); err != nil {
		return engine.Result{}, cancelled(op, err)
	}
	if reply.Err != nil {
		return engine.Result{}, wrap(op, reply.Err)
	}
	res := cloneResult(reply.Result)
	res.Final = true
	if res.AudioProcessed == 0 {
		res.AudioProcessed = duration(len(samples), rate)
	}
	return res, nil
}

// StartStream implements engine.Engine.
func (e *Engine) StartStream(ctx context.Context, opts engine.Options) (engine.Stream, error) {
	const op = "start stream"
	e.life.RLock()
	defer e.life.RUnlock()

	if err := ctx.Err(); err != nil {
		return nil, cancelled(op, err)
	}
	if !e.loaded {
		return nil, &engine.Error{Op: op, Kind: engine.ErrNotLoaded}
	}
	if !e.model.Streaming {
		return nil, invalid(op, "the model cannot stream")
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	script := pick(e.script.Streams, &e.nextStream)
	if script.StartErr != nil {
		return nil, wrap(op, script.StartErr)
	}
	s := &Stream{eng: e, script: script, opts: cloneOptions(opts)}
	e.streams = append(e.streams, s)
	e.open[s] = struct{}{}
	return s, nil
}

// Loaded reports whether a model is loaded.
func (e *Engine) Loaded() bool {
	e.life.RLock()
	defer e.life.RUnlock()
	return e.loaded
}

// Loads returns the spec of every Load call, in order.
func (e *Engine) Loads() []engine.LoadSpec {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.loads)
}

// Unloads returns how many times a loaded model was unloaded, by Unload or by
// a Load replacing it.
func (e *Engine) Unloads() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.unloads
}

// Recognitions returns every Recognize call that reached the model, in order.
func (e *Engine) Recognitions() []Recognition {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.recognitions)
}

// Streams returns every stream started, in order, open or not.
func (e *Engine) Streams() []*Stream {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.streams)
}

func (e *Engine) forget(s *Stream) {
	e.mu.Lock()
	delete(e.open, s)
	e.mu.Unlock()
}

// pick returns the script entry *next points at and advances it, repeating
// the last entry once they run out.
func pick[T any](items []T, next *int) T {
	var zero T
	if len(items) == 0 {
		return zero
	}
	i := min(*next, len(items)-1)
	*next++
	return items[i]
}

// wait sleeps for d, or until ctx is done.
func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// checkRate applies the engine's sample-rate rules and returns the effective
// rate.
func checkRate(op string, rate int) (int, error) {
	if rate == 0 {
		return SampleRate, nil
	}
	if rate < engine.MinSampleRate || rate > engine.MaxSampleRate {
		return 0, invalid(op, fmt.Sprintf("sample rate %d Hz is outside %d-%d Hz", rate, engine.MinSampleRate, engine.MaxSampleRate))
	}
	return rate, nil
}

func duration(samples, rate int) time.Duration {
	return time.Duration(samples) * time.Second / time.Duration(rate)
}

func invalid(op, detail string) error {
	return &engine.Error{Op: op, Kind: engine.ErrInvalidArgument, Detail: detail}
}

func cancelled(op string, cause error) error {
	return &engine.Error{Op: op, Kind: engine.ErrCancelled, Err: cause}
}

func wrap(op string, err error) error {
	if e, ok := errors.AsType[*engine.Error](err); ok {
		return e
	}
	return &engine.Error{Op: op, Kind: err}
}

func cloneOptions(o engine.Options) engine.Options {
	o.Phrases = slices.Clone(o.Phrases)
	return o
}

func cloneResult(r engine.Result) engine.Result {
	r.Words = slices.Clone(r.Words)
	r.Languages = slices.Clone(r.Languages)
	return r
}

// Dictation scripts a stream that hears text one word every perWord: after
// each word an interim result holds the words so far, and the end-of-stream
// final holds them all.
func Dictation(text string, perWord time.Duration) StreamScript {
	words := strings.Fields(text)
	results := make([]engine.Result, len(words))
	for i := range words {
		results[i] = engine.Result{
			Text:           strings.Join(words[:i+1], " "),
			AudioProcessed: time.Duration(i+1) * perWord,
		}
	}
	return StreamScript{
		Results: results,
		Final:   engine.Result{Text: strings.Join(words, " "), Final: true, Confidence: 1},
	}
}
