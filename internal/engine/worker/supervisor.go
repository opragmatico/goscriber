package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/notice"
)

// ErrWorkerLost is matched by the errors of calls whose worker crashed or
// stopped responding, together with engine.ErrUnavailable.
var ErrWorkerLost = errors.New("engine worker lost")

// Notices is where the supervisor tells the user about a crash or the move
// to the CPU. *notice.Hub implements it.
type Notices interface {
	Publish(n notice.Notice) bool
}

// Timeouts bound how long a worker may take to answer before it counts as
// hung. Zero fields take the defaults. Decoding on the CPU gets cpuSlack
// times longer: a slow machine must not pass for a hung worker.
type Timeouts struct {
	// Start is how long a new worker may take to answer its first request.
	// Default 30 s.
	Start time.Duration
	// Load is how long loading a model may take. Default 3 min.
	Load time.Duration
	// Call is how long a stream call or an unload may take. Default 30 s.
	Call time.Duration
	// Recognize is the least time a whole-clip decode gets; it also gets
	// AudioFactor times the clip's duration when that is longer. Defaults
	// 60 s and 10.
	Recognize   time.Duration
	AudioFactor float64
	// Heartbeat is how often an idle or busy worker is pinged, and
	// HeartbeatTimeout how long it has to answer. A worker answers pings
	// even while it decodes, so a missed one means the whole process is
	// stuck. Defaults 2 s and 10 s.
	Heartbeat        time.Duration
	HeartbeatTimeout time.Duration
	// CancelGrace is how long a cancelled decode may still take to finish
	// before its worker is ended. Default 500 ms.
	CancelGrace time.Duration
	// Stop is how long a worker may take to exit once asked. Default 2 s.
	Stop time.Duration
}

// cpuSlack multiplies the decode timeouts of a worker running on the CPU.
const cpuSlack = 4

// shutdownGrace is how long a worker whose supervisor went away waits for
// the engine to unload before it exits anyway.
const shutdownGrace = time.Second

func (t Timeouts) withDefaults() Timeouts {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&t.Start, 30*time.Second)
	def(&t.Load, 3*time.Minute)
	def(&t.Call, 30*time.Second)
	def(&t.Recognize, time.Minute)
	if t.AudioFactor <= 0 {
		t.AudioFactor = 10
	}
	def(&t.Heartbeat, 2*time.Second)
	def(&t.HeartbeatTimeout, 10*time.Second)
	def(&t.CancelGrace, 500*time.Millisecond)
	def(&t.Stop, 2*time.Second)
	return t
}

// Options configure a Supervisor.
type Options struct {
	// Command returns the command that starts a worker, without its
	// standard streams, which the supervisor connects. Nil runs this
	// executable with the engine-worker subcommand.
	Command func() (*exec.Cmd, error)
	// Notices receives a notice when a worker crashes or hangs, or when
	// transcription moves to the CPU. Nil publishes nothing.
	Notices Notices
	// Log receives the supervisor's log and the workers'. Nil means
	// slog.Default().
	Log      *slog.Logger
	Timeouts Timeouts
}

// Supervisor is an engine.Engine that runs the native engine in a worker
// process, so a crash inside NeMo-Speech.cpp (an assertion, an abort, a GPU
// driver fault) ends the worker and not the app. It is safe for concurrent
// use, and Load and Unload wait for calls in flight, as engine.Engine asks.
//
// A worker lives as long as its model: Load starts one, Unload or the next
// Load ends it, so unloading returns all the engine's memory to the OS.
//
// When a worker crashes, or stops answering within its timeout or its
// heartbeat, the supervisor publishes a notice and the next call starts a
// new worker with the same model. The call that was waiting is retried once
// in the new worker, except a decode that hung on the CPU, which is slow
// rather than broken. A stream cannot be replayed: its calls fail, and the
// caller decodes the recording another way (the dictation coordinator falls
// back to Recognize). A worker that dies or hangs while it loads or decodes
// on the GPU is a GPU fault: from then on every worker runs its model on
// the CPU, and the user hears about it once. An error the engine reports,
// such as a damaged model file, is passed on as it is.
//
// The zero value is not usable; call New.
type Supervisor struct {
	command func() (*exec.Cmd, error)
	notices Notices
	log     *slog.Logger
	t       Timeouts

	// life is held for reading by every call and for writing by Load and
	// Unload, so those wait for calls in flight.
	life sync.RWMutex
	// startMu lets one call at a time replace a lost worker.
	startMu sync.Mutex

	mu        sync.Mutex // guards what follows
	spec      *engine.LoadSpec
	w         *proc // the worker holding spec's model; nil, or dead, until the next call starts one
	gpuFailed bool  // a worker died or hung on the GPU: later ones use the CPU
}

var _ engine.Engine = (*Supervisor)(nil)

// New returns a Supervisor with no model loaded and no worker running.
func New(opts Options) *Supervisor {
	s := &Supervisor{command: opts.Command, notices: opts.Notices, log: opts.Log, t: opts.Timeouts.withDefaults()}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.command == nil {
		s.command = selfCommand
	}
	return s
}

// selfCommand runs this executable as a worker.
func selfCommand() (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return exec.Command(exe, Command), nil
}

// Reasons the supervisor ends a worker on purpose.
const (
	stopUnload   = "the model was unloaded"
	stopReplaced = "another model is loading"
)

// Load implements engine.Engine: it ends the current worker, if any, and
// starts one that loads spec. A worker that crashes or hangs while loading
// is replaced once, on the CPU when it was on the GPU.
func (s *Supervisor) Load(ctx context.Context, spec engine.LoadSpec) (engine.ModelInfo, error) {
	s.life.Lock()
	defer s.life.Unlock()

	s.mu.Lock()
	old := s.w
	s.w, s.spec = nil, nil
	s.mu.Unlock()
	if old != nil {
		old.shutdown(stopReplaced, s.t.Stop)
	}
	if err := ctx.Err(); err != nil {
		return engine.ModelInfo{}, cancelled("load", err)
	}
	p, info, err := s.startWorker(ctx, spec)
	if err != nil {
		return engine.ModelInfo{}, err
	}
	s.mu.Lock()
	s.spec, s.w = &spec, p
	s.mu.Unlock()
	return info, nil
}

// Unload implements engine.Engine: it ends the worker, which frees the
// model and every byte the engine held. Open streams count as closed.
func (s *Supervisor) Unload() error {
	s.life.Lock()
	defer s.life.Unlock()
	s.mu.Lock()
	p := s.w
	s.w, s.spec = nil, nil
	s.mu.Unlock()
	if p != nil {
		p.shutdown(stopUnload, s.t.Stop)
	}
	return nil
}

// Recognize implements engine.Engine. Cancelling ctx gives the decode
// CancelGrace to finish, then ends the worker, since NeMo-Speech.cpp cannot
// stop a decode; the next call starts a new one.
func (s *Supervisor) Recognize(ctx context.Context, samples []float32, sampleRate int, opts engine.Options) (engine.Result, error) {
	const opName = "recognize"
	s.life.RLock()
	defer s.life.RUnlock()
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return engine.Result{}, cancelled(opName, err)
		}
		p, err := s.ensure(ctx)
		if err != nil {
			return engine.Result{}, err
		}
		timeout := s.decodeTimeout(p, s.t.Recognize, audioLength(len(samples), sampleRate))
		resp, err := p.do(ctx, request{Op: opRecognize, Rate: sampleRate, Options: &opts}, samples, timeout, s.t.CancelGrace)
		if err == nil {
			if err := resp.Err.err(); err != nil {
				return engine.Result{}, err
			}
			if resp.Result == nil {
				return engine.Result{}, &engine.Error{Op: opName, Kind: engine.ErrRuntime, Detail: "the engine worker sent no result"}
			}
			return *resp.Result, nil
		}
		if !s.retry(ctx, err, attempt) {
			return engine.Result{}, callError(ctx, opName, err)
		}
		s.log.Warn("retrying the recognition in a new engine worker", "err", err)
	}
}

// StartStream implements engine.Engine. The stream lives in the current
// worker: if that worker is lost, the stream's calls fail with an error
// matching ErrWorkerLost and engine.ErrUnavailable.
func (s *Supervisor) StartStream(ctx context.Context, opts engine.Options) (engine.Stream, error) {
	const opName = "start stream"
	s.life.RLock()
	defer s.life.RUnlock()
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, cancelled(opName, err)
		}
		p, err := s.ensure(ctx)
		if err != nil {
			return nil, err
		}
		resp, err := p.do(ctx, request{Op: opStreamStart, Options: &opts}, nil, s.decodeTimeout(p, s.t.Call, 0), s.t.CancelGrace)
		if err == nil {
			if err := resp.Err.err(); err != nil {
				return nil, err
			}
			return &stream{s: s, p: p, id: resp.Stream}, nil
		}
		if !s.retry(ctx, err, attempt) {
			return nil, callError(ctx, opName, err)
		}
		s.log.Warn("retrying the stream's start in a new engine worker", "err", err)
	}
}

// ensure returns the worker holding the model, starting a new one when the
// last was lost. s.life must be held for reading.
func (s *Supervisor) ensure(ctx context.Context) (*proc, error) {
	s.mu.Lock()
	p, loaded := s.w, s.spec != nil
	s.mu.Unlock()
	if !loaded {
		return nil, &engine.Error{Op: "load", Kind: engine.ErrNotLoaded}
	}
	if p != nil && p.alive() {
		return p, nil
	}

	s.startMu.Lock()
	defer s.startMu.Unlock()
	s.mu.Lock()
	p, spec := s.w, s.spec
	s.mu.Unlock()
	if p != nil && p.alive() {
		return p, nil
	}
	if spec == nil {
		return nil, &engine.Error{Op: "load", Kind: engine.ErrNotLoaded}
	}
	s.log.Info("starting a new engine worker", "model", spec.ID)
	p, _, err := s.startWorker(ctx, *spec)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.w = p
	s.mu.Unlock()
	return p, nil
}

// startWorker starts a worker and loads spec into it: on the CPU once the
// GPU has failed. A worker lost while loading is replaced once, except
// after a hang on the CPU.
func (s *Supervisor) startWorker(ctx context.Context, spec engine.LoadSpec) (*proc, engine.ModelInfo, error) {
	for attempt := 0; ; attempt++ {
		run := spec
		s.mu.Lock()
		if s.gpuFailed && !run.CPU {
			run.CPU = true
			s.log.Info("loading the model on the CPU: the GPU failed earlier", "model", spec.ID)
		}
		s.mu.Unlock()
		p, info, err := s.spawn(ctx, run)
		if err == nil {
			return p, info, nil
		}
		if !s.retry(ctx, err, attempt) {
			return nil, engine.ModelInfo{}, callError(ctx, "load", err)
		}
		s.log.Warn("starting another engine worker", "model", spec.ID, "err", err)
	}
}

// spawn starts a worker process and loads spec into it.
func (s *Supervisor) spawn(ctx context.Context, spec engine.LoadSpec) (*proc, engine.ModelInfo, error) {
	cmd, err := s.command()
	if err != nil {
		return nil, engine.ModelInfo{}, unavailable("load", "the engine worker cannot be started", err)
	}
	began := time.Now()
	p, err := start(cmd, spec.CPU, s.log, s.workerDied)
	if err != nil {
		return nil, engine.ModelInfo{}, unavailable("load", "the engine worker cannot be started", err)
	}
	p.log.Debug("engine worker started", "cpu", spec.CPU)

	resp, err := p.do(ctx, request{Op: opHello, Version: protocolVersion}, nil, s.t.Start, 0)
	if err != nil {
		return nil, engine.ModelInfo{}, err
	}
	if resp.Version != protocolVersion {
		p.shutdown("another version", s.t.Stop)
		return nil, engine.ModelInfo{}, unavailable("load",
			fmt.Sprintf("the engine worker speaks protocol %d, not %d: the goscriber executable changed; restart Goscriber",
				resp.Version, protocolVersion), nil)
	}
	resp, err = p.do(ctx, request{Op: opLoad, Spec: &spec}, nil, s.t.Load, 0)
	if err != nil {
		return nil, engine.ModelInfo{}, err
	}
	if err := resp.Err.err(); err != nil {
		p.shutdown("the model failed to load", s.t.Stop)
		return nil, engine.ModelInfo{}, err
	}
	if resp.Info == nil {
		p.shutdown("no model information", s.t.Stop)
		return nil, engine.ModelInfo{}, &engine.Error{Op: "load", Kind: engine.ErrRuntime, Detail: "the engine worker sent no model information"}
	}
	if resp.Info.CPU && !spec.CPU {
		p.log.Info("the model runs on the CPU: the engine has no GPU backend, or no GPU could take the model", "model", spec.ID)
	}
	p.log.Info("engine worker ready", "model", spec.ID, "cpu", resp.Info.CPU, "took", time.Since(began).Round(time.Millisecond))
	go s.heartbeat(p)
	return p, *resp.Info, nil
}

// heartbeat pings p until it is gone. A ping it does not answer in time
// ends it as hung.
func (s *Supervisor) heartbeat(p *proc) {
	t := time.NewTicker(s.t.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-p.dead:
			return
		case <-t.C:
		}
		if _, err := p.do(context.Background(), request{Op: opPing}, nil, s.t.HeartbeatTimeout, 0); err != nil {
			return
		}
	}
}

// workerDied runs once a worker process is gone, before the calls waiting
// on it fail. busy is the work it was doing, or "" when it was idle.
func (s *Supervisor) workerDied(p *proc, cause error, busy op) {
	s.mu.Lock()
	if s.w == p {
		s.w = nil
	}
	p.mu.Lock()
	stopped, hung := p.stop, p.hung
	p.mu.Unlock()
	if stopped != "" {
		s.mu.Unlock()
		p.log.Debug("engine worker stopped", "why", stopped)
		return
	}
	// A worker that dies before it is given a model never touched the GPU.
	gpuFault := busy != "" && busy != opHello && !p.cpu && !s.gpuFailed
	if gpuFault {
		s.gpuFailed = true
	}
	s.mu.Unlock()

	what := "crashed"
	if hung != "" {
		what = "stopped responding"
	}
	doing := "while idle"
	if busy != "" {
		doing = "while " + activity(busy)
	}
	p.log.Error("the engine worker "+what+" "+doing, "err", cause, "hung", hung, "cpu", p.cpu, "stderr_tail", p.stderr.get())
	if s.notices == nil {
		return
	}
	if gpuFault {
		s.notices.Publish(notice.EngineCPUFallback(fmt.Sprintf("The speech engine %s %s on the GPU.", what, doing)))
		return
	}
	s.notices.Publish(notice.EngineCrashed(fmt.Sprintf("It %s %s.", what, doing)))
}

// activity describes what a worker was doing for op, for a notice.
func activity(o op) string {
	switch o {
	case opHello:
		return "starting"
	case opLoad:
		return "loading the model"
	case opUnload:
		return "unloading the model"
	default:
		return "transcribing"
	}
}

// retry reports whether a call that failed with err may be tried again in
// a new worker: once, when its worker was lost, unless it hung on the CPU.
func (s *Supervisor) retry(ctx context.Context, err error, attempt int) bool {
	if attempt > 0 || ctx.Err() != nil {
		return false
	}
	lost, ok := errors.AsType[*lostError](err)
	return ok && (lost.hung == "" || !lost.cpu)
}

// callError turns the error of a call that will not be retried into an
// *engine.Error.
func callError(ctx context.Context, opName string, err error) error {
	if ctx.Err() != nil {
		return cancelled(opName, ctx.Err())
	}
	return wrapError(opName, err)
}

func wrapError(opName string, err error) error {
	if lost, ok := errors.AsType[*lostError](err); ok {
		return &engine.Error{Op: opName, Kind: engine.ErrUnavailable, Detail: lost.Error(), Err: ErrWorkerLost}
	}
	if errors.Is(err, errFrame) {
		return &engine.Error{Op: opName, Kind: engine.ErrInvalidArgument, Detail: err.Error()}
	}
	if _, ok := errors.AsType[*engine.Error](err); ok {
		return err
	}
	return &engine.Error{Op: opName, Kind: engine.ErrRuntime, Err: err}
}

// decodeTimeout is how long p may take over work on audio of length d:
// floor, or AudioFactor times d when longer, and cpuSlack times that on the
// CPU.
func (s *Supervisor) decodeTimeout(p *proc, floor, d time.Duration) time.Duration {
	t := max(floor, time.Duration(float64(d)*s.t.AudioFactor))
	if p.cpu {
		t *= cpuSlack
	}
	return t
}

// audioLength is how long n samples at rate last; rate 0 is the models'
// own 16 kHz.
func audioLength(n, rate int) time.Duration {
	if rate <= 0 {
		rate = 16000
	}
	return time.Duration(n) * time.Second / time.Duration(rate)
}

func cancelled(opName string, cause error) error {
	return &engine.Error{Op: opName, Kind: engine.ErrCancelled, Err: cause}
}

func unavailable(opName, detail string, cause error) error {
	return &engine.Error{Op: opName, Kind: engine.ErrUnavailable, Detail: detail, Err: cause}
}

// stream is a stream open in a worker.
type stream struct {
	s  *Supervisor
	p  *proc
	id uint64

	mu     sync.Mutex
	closed bool
}

var _ engine.Stream = (*stream)(nil)

// call sends a request on the stream and waits for the answer.
func (st *stream) call(o op, opName string, samples []float32, rate int) (response, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed {
		return response{}, &engine.Error{Op: opName, Kind: engine.ErrInvalidArgument, Detail: "the stream is closed"}
	}
	st.s.life.RLock()
	defer st.s.life.RUnlock()
	timeout := st.s.decodeTimeout(st.p, st.s.t.Call, audioLength(len(samples), rate))
	resp, err := st.p.do(context.Background(), request{Op: o, Stream: st.id, Rate: rate}, samples, timeout, 0)
	if err != nil {
		if lost, ok := errors.AsType[*lostError](err); ok && (lost.stopped == stopUnload || lost.stopped == stopReplaced) {
			// Unloading closes the streams, as in the engine itself.
			return response{}, &engine.Error{Op: opName, Kind: engine.ErrInvalidArgument, Detail: "the stream is closed"}
		}
		return response{}, wrapError(opName, err)
	}
	return resp, resp.Err.err()
}

// Push implements engine.Stream.
func (st *stream) Push(samples []float32, sampleRate int) error {
	_, err := st.call(opStreamPush, "stream push", samples, sampleRate)
	return err
}

// Next implements engine.Stream.
func (st *stream) Next() (*engine.Result, error) {
	resp, err := st.call(opStreamNext, "stream next", nil, 0)
	switch {
	case err != nil:
		return nil, err
	case resp.EOF:
		return nil, io.EOF
	default:
		return resp.Result, nil
	}
}

// ForceEndpoint implements engine.Stream.
func (st *stream) ForceEndpoint() error {
	_, err := st.call(opStreamEnd, "stream force endpoint", nil, 0)
	return err
}

// Finish implements engine.Stream.
func (st *stream) Finish() error {
	_, err := st.call(opStreamFinish, "stream finish", nil, 0)
	return err
}

// Close implements engine.Stream. A stream whose worker is gone closes
// without error.
func (st *stream) Close() error {
	_, err := st.call(opStreamClose, "stream close", nil, 0)
	st.mu.Lock()
	st.closed = true
	st.mu.Unlock()
	if errors.Is(err, ErrWorkerLost) || errors.Is(err, engine.ErrInvalidArgument) {
		return nil
	}
	return err
}
