// Package autounload frees an idle model's memory and loads it again when it
// is next needed.
//
// An Engine wraps another engine.Engine, such as the NeMo-Speech.cpp binding
// or the worker supervisor, and implements the same interface. It counts the
// model as busy while a Recognize is in flight, a stream is open, or the
// caller holds it with Acquire. Once the model has been idle for the timeout,
// the Engine unloads the inner engine but keeps the LoadSpec, and the next
// call loads that spec again before it proceeds. That first call after an
// idle unload therefore pays for the load; the dictation coordinator hides
// it by calling Acquire when recording starts.
package autounload

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/opragmatico/goscriber/internal/engine"
)

// Engine is an engine.Engine that unloads its inner engine's model after a
// period of inactivity. It is safe for concurrent use. The zero value is not
// usable; call New.
type Engine struct {
	inner engine.Engine
	log   *slog.Logger

	// sem is held while the inner engine loads or unloads, and while a call
	// checks that the model is resident and counts itself busy, so an idle
	// unload never slips between the two. Callers wait for it with their
	// context. Calls ending take only mu, so an inner Unload can wait for
	// them while sem is held.
	sem chan struct{}

	mu       sync.Mutex       // guards what follows; never held across a call into inner
	spec     *engine.LoadSpec // the model to keep available; nil before Load and after Unload
	info     engine.ModelInfo // what the model reported when it was last loaded
	resident bool             // the inner engine holds the model
	busy     int              // recognitions in flight, open streams and held Acquires
	timeout  time.Duration    // idle time before an unload; negative means never
	timer    *time.Timer      // the pending idle unload, if any
	armed    uint64           // bumped whenever the timer is replaced or stopped
}

var _ engine.Engine = (*Engine)(nil)

// Never is a timeout that keeps an idle model loaded. Any negative timeout
// means the same.
const Never time.Duration = -1

// New returns an Engine over inner, which must have no model loaded, that
// unloads the model once it has been idle for timeout. A timeout of 0
// unloads it as soon as it is idle; Never keeps it loaded. A nil log means
// slog.Default().
func New(inner engine.Engine, timeout time.Duration, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{inner: inner, log: log, sem: make(chan struct{}, 1), timeout: timeout}
}

// Load implements engine.Engine. The model counts as idle from the moment it
// is loaded, so one that is never used is unloaded after the timeout too.
func (e *Engine) Load(ctx context.Context, spec engine.LoadSpec) (engine.ModelInfo, error) {
	if err := e.lock(ctx); err != nil {
		return engine.ModelInfo{}, cancelled("load", err)
	}
	defer e.unlock()

	e.mu.Lock()
	e.disarmLocked()
	e.mu.Unlock()
	info, err := e.inner.Load(ctx, spec)

	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		// The inner engine unloads the old model before it loads a new one.
		e.spec, e.info, e.resident = nil, engine.ModelInfo{}, false
		return engine.ModelInfo{}, err
	}
	e.spec, e.info, e.resident = &spec, cloneInfo(info), true
	e.armLocked()
	return info, nil
}

// Unload implements engine.Engine. Unlike an idle unload it forgets the
// model: later calls fail with engine.ErrNotLoaded until the next Load.
// Streams still open are closed by the inner engine but count as busy until
// their Close.
func (e *Engine) Unload() error {
	e.sem <- struct{}{}
	defer e.unlock()

	e.mu.Lock()
	e.disarmLocked()
	e.spec, e.info, e.resident = nil, engine.ModelInfo{}, false
	e.mu.Unlock()
	return e.inner.Unload()
}

// Recognize implements engine.Engine, reloading the model first if it was
// unloaded for being idle.
func (e *Engine) Recognize(ctx context.Context, samples []float32, sampleRate int, opts engine.Options) (engine.Result, error) {
	if err := e.begin(ctx, "recognize"); err != nil {
		return engine.Result{}, err
	}
	defer e.end()
	return e.inner.Recognize(ctx, samples, sampleRate, opts)
}

// StartStream implements engine.Engine, reloading the model first if it was
// unloaded for being idle. The model stays loaded until the stream is closed.
func (e *Engine) StartStream(ctx context.Context, opts engine.Options) (engine.Stream, error) {
	if err := e.begin(ctx, "start stream"); err != nil {
		return nil, err
	}
	s, err := e.inner.StartStream(ctx, opts)
	if err != nil {
		e.end()
		return nil, err
	}
	return &stream{Stream: s, release: sync.OnceFunc(e.end)}, nil
}

// Acquire loads the model if it was unloaded for being idle and keeps it
// loaded until release is called. The coordinator acquires the model when
// recording starts, so a reload overlaps the user speaking and a long
// recording, during which the engine has nothing to do, cannot outlast the
// timeout. Calling release more than once is safe.
func (e *Engine) Acquire(ctx context.Context) (release func(), err error) {
	if err := e.begin(ctx, "acquire"); err != nil {
		return nil, err
	}
	return sync.OnceFunc(e.end), nil
}

// SetTimeout changes how long the model may stay idle, as for New. When the
// model is idle now, the new timeout counts from this call. Setting the
// current timeout again changes nothing, so the idle time so far is kept.
func (e *Engine) SetTimeout(timeout time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if timeout == e.timeout || (timeout < 0 && e.timeout < 0) {
		return
	}
	e.timeout = timeout
	e.armLocked()
}

// Model returns what the selected model can do, and false when none is
// selected (before Load, after Unload or a failed Load). A model unloaded for
// being idle is still selected: the next call loads it again.
func (e *Engine) Model() (engine.ModelInfo, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneInfo(e.info), e.spec != nil
}

// Resident reports whether the model is in memory now.
func (e *Engine) Resident() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.resident
}

// begin makes sure the model is loaded, reloading it after an idle unload,
// and counts a call as busy until end.
func (e *Engine) begin(ctx context.Context, op string) error {
	if err := e.lock(ctx); err != nil {
		return cancelled(op, err)
	}
	defer e.unlock()

	e.mu.Lock()
	spec, resident := e.spec, e.resident
	e.mu.Unlock()
	if spec == nil {
		return &engine.Error{Op: op, Kind: engine.ErrNotLoaded}
	}
	if !resident {
		if err := e.reload(ctx, *spec); err != nil {
			return err
		}
	}

	e.mu.Lock()
	e.busy++
	e.disarmLocked()
	e.mu.Unlock()
	return nil
}

// reload loads spec again after an idle unload. sem must be held.
func (e *Engine) reload(ctx context.Context, spec engine.LoadSpec) error {
	start := time.Now()
	info, err := e.inner.Load(ctx, spec)
	if err != nil {
		// The model stays selected, so the next call tries again.
		e.log.Warn("reloading the model after an idle unload failed", "model", spec.ID, "err", err)
		return err
	}
	e.mu.Lock()
	e.info, e.resident = cloneInfo(info), true
	e.mu.Unlock()
	e.log.Info("model reloaded after an idle unload", "model", spec.ID, "took", time.Since(start).Round(time.Millisecond))
	return nil
}

// end marks a call begun by begin as finished, and starts the idle timer when
// it was the last one.
func (e *Engine) end() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.busy--
	e.armLocked()
}

// armLocked starts the idle timer from now when the model is loaded and idle
// and a timeout is set, replacing any pending one. e.mu must be held.
func (e *Engine) armLocked() {
	e.disarmLocked()
	if e.timeout < 0 || !e.resident || e.busy > 0 {
		return
	}
	id := e.armed
	e.timer = time.AfterFunc(e.timeout, func() { e.expire(id) })
}

// disarmLocked cancels the pending idle unload. A timer that has already
// fired sees that e.armed moved on and does nothing. e.mu must be held.
func (e *Engine) disarmLocked() {
	e.armed++
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
}

// expire unloads the model when timer id is still the current one, so
// nothing has used the model or changed the timeout since it was set.
func (e *Engine) expire(id uint64) {
	e.sem <- struct{}{}
	defer e.unlock()

	e.mu.Lock()
	if id != e.armed || e.busy > 0 || !e.resident {
		e.mu.Unlock()
		return
	}
	e.timer, e.resident = nil, false
	model, idle := e.spec.ID, e.timeout
	e.mu.Unlock()

	// Holding sem keeps new calls out until the model is gone; they then
	// reload it.
	if err := e.inner.Unload(); err != nil {
		e.log.Warn("unloading the idle model failed", "model", model, "err", err)
		return
	}
	e.log.Info("model unloaded after being idle", "model", model, "idle", idle)
}

// lock takes sem, giving up when ctx is done first.
func (e *Engine) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case e.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) unlock() { <-e.sem }

// stream is an open stream of the inner engine; the model counts as busy
// until it is closed.
type stream struct {
	engine.Stream
	release func()
}

// Close implements engine.Stream.
func (s *stream) Close() error {
	err := s.Stream.Close()
	s.release()
	return err
}

func cancelled(op string, cause error) error {
	return &engine.Error{Op: op, Kind: engine.ErrCancelled, Err: cause}
}

func cloneInfo(info engine.ModelInfo) engine.ModelInfo {
	info.Languages = slices.Clone(info.Languages)
	return info
}
