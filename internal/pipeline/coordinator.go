package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/output"
)

// State is where dictation stands.
type State int

// States, in the order a session goes through them. A cancel returns to
// StateIdle from any of them.
const (
	StateIdle State = iota
	StateRecording
	StateTranscribing
	StatePostProcessing
	StateOutputting
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateRecording:
		return "recording"
	case StateTranscribing:
		return "transcribing"
	case StatePostProcessing:
		return "post_processing"
	case StateOutputting:
		return "outputting"
	default:
		return fmt.Sprintf("State(%d)", int(s))
	}
}

// Action is what the shortcut, the tray or the command line asks of
// dictation.
type Action int

// Actions.
const (
	ActionStart  Action = iota + 1 // start recording
	ActionStop                     // stop recording and transcribe it
	ActionToggle                   // start when idle, stop when recording
	ActionCancel                   // end the session and discard it
)

func (a Action) String() string {
	switch a {
	case ActionStart:
		return "start"
	case ActionStop:
		return "stop"
	case ActionToggle:
		return "toggle"
	case ActionCancel:
		return "cancel"
	default:
		return "none"
	}
}

// Recorder records the microphone. *audio.Recorder implements it.
type Recorder interface {
	// Start begins a recording.
	Start() error
	// Stop ends the recording and returns its samples, mono at
	// SampleRate Hz.
	Stop() []float32
	// SampleRate is the rate of the current or latest recording.
	SampleRate() int
	// Frames delivers the current recording's samples as they arrive, and
	// closes once Stop has delivered them all: the chunks add up to what
	// Stop returns. The reader must drain it.
	Frames() <-chan []float32
	// Levels delivers the recording's level about audio.LevelRate times a
	// second. The channel lives as long as the Recorder.
	Levels() <-chan audio.Level
}

// Output delivers a transcript to the focused app. *output.Outputter
// implements it.
type Output interface {
	Deliver(ctx context.Context, text string, opts output.Options) error
}

// Notices is where problems are reported. *notice.Hub implements it.
type Notices interface {
	Publish(n notice.Notice) bool
	Resolve(code notice.Code) bool
}

// Acquirer is an engine that can be held loaded, such as
// *autounload.Engine. The coordinator holds it from the start of a recording
// until the transcript is out, so a reload after an idle unload overlaps the
// user speaking.
type Acquirer interface {
	Acquire(ctx context.Context) (release func(), err error)
}

// Feedback plays the start and stop cues and mutes other audio while
// recording. *audio.Feedback implements it.
type Feedback interface {
	// Start runs just before capture starts, and reports whether a cue
	// played.
	Start(o audio.FeedbackOptions) (played bool)
	// Stop runs once capture has stopped, also after a cancel or a failed
	// start, so other audio is always put back.
	Stop(o audio.FeedbackOptions, cancelled bool)
}

// preRollDropper is a recorder whose always-on microphone keeps audio from
// before Start, such as *audio.Recorder.
type preRollDropper interface{ DropPreRoll() }

// FeedbackOptions are the feedback the settings ask for.
func FeedbackOptions(s config.Settings) audio.FeedbackOptions {
	return audio.FeedbackOptions{
		Sounds: s.AudioFeedback,
		Volume: s.AudioFeedbackVolume,
		Device: s.OutputDevice,
		Mute:   s.MuteWhileRecording,
	}
}

// Options configure a Coordinator. Every field but Feedback, Stages and Log
// is required.
type Options struct {
	Recorder Recorder
	// Engine transcribes. The coordinator loads the model into it on the
	// first recording and whenever the selected model changes, and holds it
	// with Acquire when the engine is an Acquirer.
	Engine engine.Engine
	Models ModelSource
	Output Output
	// Notices receives the problems the user must hear about: no model,
	// the microphone, the engine, the delivery.
	Notices Notices
	// Settings returns the current settings. Each session reads them once,
	// when it starts.
	Settings func() config.Settings
	// Feedback, if set, plays the cues and mutes other audio around each
	// recording.
	Feedback Feedback
	// MicrophoneAccess, if set, is asked before each recording whether the
	// app may use the microphone; it may ask the user and wait for the
	// answer. False refuses the start with the microphone-permission
	// notice: macOS would record silence.
	MicrophoneAccess func() bool
	Stages           Stages
	// Log receives a line per session and, at debug level, the timing of
	// every stage. Nil means slog's default logger.
	Log *slog.Logger
}

// Coordinator runs dictation: one session at a time goes from recording
// through transcription, the text stages and output, then back to idle.
//
// Its actions never block, so they are safe on the UI thread. They run in
// order on the coordinator's own goroutine; the model loads and decodes on a
// second goroutine that owns the engine; the text stages and the delivery
// run on a third, per session. Every session has an ID, and anything that
// finishes after its session was cancelled is dropped. Follow the state with
// Subscribe.
//
// Recordings are transcribed while they go on. A streaming model decodes the
// audio through a stream as it arrives, and its interim results are
// published as EventInterim; on Stop the stream is finished and its final is
// the transcript. An offline model, with long_form_chunking, decodes the
// recording in chunks cut at pauses while it goes on, and on Stop only the
// open chunk. Either way, if decoding live fails, the recording is decoded
// whole on Stop instead.
type Coordinator struct {
	rec      Recorder
	eng      engine.Engine
	models   ModelSource
	out      Output
	notices  Notices
	settings func() config.Settings
	feedback Feedback
	micOK    func() bool
	stages   Stages
	log      *slog.Logger

	actions *serial // runs the actions
	jobs    *serial // the engine goroutine: loads and decodes, in order
	done    chan struct{}
	tasks   sync.WaitGroup // the level forwarder and the per-session finishers

	mu     sync.Mutex // guards what follows
	state  State
	sess   *session // the session in progress, or nil when idle
	lastID uint64
	subs   map[*subscriber]struct{}
	closed bool

	// loaded is the spec the engine holds, or nil, and info what the model
	// can do. Only the engine goroutine touches them.
	loaded *engine.LoadSpec
	info   engine.ModelInfo
}

// session is one dictation, from Start to idle.
type session struct {
	id       uint64
	ctx      context.Context // cancelled when the session ends
	cancel   context.CancelFunc
	settings config.Settings
	model    Model
	started  time.Time
	frames   <-chan []float32 // the recording's audio as it arrives
	rate     int              // of the recording

	// Written by one goroutine at a time, as the session moves on.
	prepErr error       // why the model could not be made ready; engine goroutine only
	live    liveDecoder // decodes the recording as it goes on, or nil; engine goroutine only
	timings timings

	mu      sync.Mutex
	ended   bool
	release func() // lets go of the engine, once acquired
}

// timings are how long each stage of a session took.
type timings struct {
	recording, load, transcribe, cleanup, llm, hook, output time.Duration
}

// New returns a Coordinator, idle. Close it when done.
func New(opts Options) *Coordinator {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	c := &Coordinator{
		rec:      opts.Recorder,
		eng:      opts.Engine,
		models:   opts.Models,
		out:      opts.Output,
		notices:  opts.Notices,
		settings: opts.Settings,
		feedback: opts.Feedback,
		micOK:    opts.MicrophoneAccess,
		stages:   opts.Stages,
		log:      log,
		actions:  newSerial(),
		jobs:     newSerial(),
		done:     make(chan struct{}),
		subs:     map[*subscriber]struct{}{},
	}
	levels := c.rec.Levels()
	c.tasks.Go(func() { c.forwardLevels(levels) })
	return c
}

// Start begins recording, unless a session is already in progress. With no
// model ready it does not record: it raises the "no model downloaded"
// notice instead.
func (c *Coordinator) Start() { c.do(ActionStart) }

// Stop ends the recording and transcribes it.
func (c *Coordinator) Stop() { c.do(ActionStop) }

// Toggle starts recording when idle and stops it when recording. While the
// transcript is on its way it does nothing.
func (c *Coordinator) Toggle() { c.do(ActionToggle) }

// Cancel ends the session in progress and discards it: the recording, or
// the transcript on its way.
func (c *Coordinator) Cancel() { c.do(ActionCancel) }

// Do carries out a.
func (c *Coordinator) Do(a Action) { c.do(a) }

func (c *Coordinator) do(a Action) {
	c.actions.post(func() {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return
		}
		switch a {
		case ActionStart:
			c.start()
		case ActionStop:
			c.stop()
		case ActionToggle:
			c.toggle()
		case ActionCancel:
			c.cancel()
		}
	})
}

// State returns the current state.
func (c *Coordinator) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Subscribe returns a channel of every event from now on, in order, and a
// function that ends the subscription and closes the channel. Events wait in
// the subscriber's own queue, so a slow reader holds up nobody; it only
// misses levels superseded by newer ones.
func (c *Coordinator) Subscribe() (<-chan Event, func()) {
	s := newSubscriber()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		s.close()
		return s.out, func() {}
	}
	c.subs[s] = struct{}{}
	c.mu.Unlock()
	var once sync.Once
	return s.out, func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.subs, s)
			c.mu.Unlock()
			s.close()
		})
	}
}

// Close discards the session in progress, waits for the coordinator's
// goroutines and ends every subscription. A decode in progress may not stop
// at once: Close waits for it. Actions after Close do nothing.
func (c *Coordinator) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

	c.actions.close()
	c.mu.Lock()
	s, st := c.sess, c.state
	if s != nil {
		c.sess = nil
		c.setStateLocked(StateIdle, s.id)
	}
	c.mu.Unlock()
	if s != nil {
		if st == StateRecording {
			c.rec.Stop()
			c.feedbackStop(s.settings, true)
		}
		s.end()
		c.discardLive(s)
		c.log.Info("dictation discarded: Goscriber is quitting", "session", s.id, "state", st.String())
	}
	close(c.done)
	c.jobs.close()
	c.tasks.Wait()

	c.mu.Lock()
	subs := c.subs
	c.subs = nil
	c.mu.Unlock()
	for sub := range subs {
		sub.close()
	}
}

// start runs on the actions goroutine.
func (c *Coordinator) start() {
	c.mu.Lock()
	if c.state != StateIdle {
		c.refuseLocked(ActionStart, "a session is in progress")
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	settings := c.settings()
	model, err := c.models.Resolve(settings)
	if err != nil {
		c.modelUnavailable(model, err)
		c.refuse(ActionStart, "no model is ready")
		return
	}
	if c.micOK != nil && !c.micOK() {
		c.log.Info("dictation cannot start: no microphone permission")
		c.notices.Resolve(notice.CodeMicrophoneDenied)
		c.notices.Publish(notice.MicrophoneDenied())
		c.refuse(ActionStart, "no microphone permission")
		return
	}
	// The start cue plays to its end before capture starts, so the
	// microphone does not record it.
	if c.feedback != nil && c.feedback.Start(FeedbackOptions(settings)) {
		if d, ok := c.rec.(preRollDropper); ok {
			d.DropPreRoll()
		}
	}
	if err := c.rec.Start(); err != nil {
		c.feedbackStop(settings, true)
		c.notices.Publish(notice.MicrophoneUnavailable(fmt.Sprintf("The microphone could not be opened: %v.", err)))
		c.refuse(ActionStart, "the microphone could not be opened")
		return
	}
	frames, rate := c.rec.Frames(), c.rec.SampleRate()

	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.lastID++
	s := &session{
		id:       c.lastID,
		ctx:      ctx,
		cancel:   cancel,
		settings: settings,
		model:    model,
		started:  time.Now(),
		frames:   frames,
		rate:     rate,
	}
	c.sess = s
	c.setStateLocked(StateRecording, s.id)
	c.mu.Unlock()
	c.log.Info("dictation started", "session", s.id, "model", model.Spec.ID)
	c.jobs.post(func() { c.prepare(s) })
	c.jobs.post(func() { c.pump(s) })
}

// modelUnavailable tells the user why Start cannot record. The notice is
// raised afresh even when it is already active: the user just asked for
// dictation and needs to hear why nothing happens.
func (c *Coordinator) modelUnavailable(m Model, err error) {
	c.log.Info("dictation cannot start", "err", err)
	n := notice.NoModel()
	if errors.Is(err, ErrModelCorrupt) {
		n = notice.ModelCorrupt(displayName(m))
	}
	c.notices.Resolve(n.Code)
	c.notices.Publish(n)
}

// stop runs on the actions goroutine.
func (c *Coordinator) stop() {
	c.mu.Lock()
	s := c.sess
	if c.state != StateRecording {
		c.refuseLocked(ActionStop, "not recording")
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	samples := c.rec.Stop()
	rate := c.rec.SampleRate()
	c.feedbackStop(s.settings, false)
	s.timings.recording = time.Since(s.started)
	c.stageDone(s, "recording", s.timings.recording)

	if err := GateFromSettings(s.settings).Check(samples, rate); err != nil {
		c.mu.Lock()
		c.endLocked(s)
		c.mu.Unlock()
		s.end()
		c.discardLive(s)
		c.log.Debug("dictation ended without speech", "session", s.id, "reason", err)
		if len(samples) > 0 && audio.Measure(samples).Peak == 0 {
			// Real microphones always pick up some noise: exact zeros are
			// what macOS delivers without the permission, or a muted or
			// wrong input device.
			c.log.Warn("the microphone recorded nothing but digital silence; check the microphone permission and the input device",
				"session", s.id)
		}
		return
	}
	c.mu.Lock()
	c.setStateLocked(StateTranscribing, s.id)
	c.mu.Unlock()
	c.jobs.post(func() { c.transcribe(s, samples, rate) })
}

// toggle runs on the actions goroutine.
func (c *Coordinator) toggle() {
	switch c.State() {
	case StateIdle:
		c.start()
	case StateRecording:
		c.stop()
	default:
		c.refuse(ActionToggle, "the transcript is on its way")
	}
}

// cancel runs on the actions goroutine.
func (c *Coordinator) cancel() {
	c.mu.Lock()
	s, st := c.sess, c.state
	if s == nil {
		c.refuseLocked(ActionCancel, "nothing to cancel")
		c.mu.Unlock()
		return
	}
	c.endLocked(s)
	c.mu.Unlock()
	if st == StateRecording {
		c.rec.Stop()
		c.feedbackStop(s.settings, true)
	}
	s.end()
	c.discardLive(s)
	c.log.Info("dictation cancelled", "session", s.id, "state", st.String())
}

// feedbackStop puts other audio back and, unless cancelled, plays the stop
// cue.
func (c *Coordinator) feedbackStop(s config.Settings, cancelled bool) {
	if c.feedback != nil {
		c.feedback.Stop(FeedbackOptions(s), cancelled)
	}
}

// prepare loads the session's model if the engine does not hold it, and
// holds it loaded until the session ends. It runs on the engine goroutine
// while the user speaks.
func (c *Coordinator) prepare(s *session) {
	if s.ctx.Err() != nil {
		return
	}
	spec := s.model.Spec
	if c.loaded == nil || *c.loaded != spec {
		start := time.Now()
		info, err := c.eng.Load(s.ctx, spec)
		s.timings.load = time.Since(start)
		if err != nil {
			c.loaded, c.info = nil, engine.ModelInfo{}
			if s.ctx.Err() == nil {
				s.prepErr = &loadError{err: err}
			}
			return
		}
		c.loaded, c.info = &spec, info
		c.stageDone(s, "load", s.timings.load)
	}
	if a, ok := c.eng.(Acquirer); ok {
		release, err := a.Acquire(s.ctx)
		if err != nil {
			if s.ctx.Err() == nil {
				s.prepErr = &loadError{err: err}
			}
			return
		}
		s.hold(release)
	}
}

// pump decodes the recording while it goes on: it feeds the audio to a live
// decoder as it arrives and publishes the transcript so far. It runs on the
// engine goroutine after prepare, until the recording stops, and always
// drains the recording's frames.
func (c *Coordinator) pump(s *session) {
	if s.ctx.Err() == nil && s.prepErr == nil {
		s.live = c.startLive(s)
	}
	for samples := range s.frames {
		if s.live == nil || s.ctx.Err() != nil {
			continue
		}
		if sofar, changed := s.live.push(samples); changed {
			c.publishInterim(s, sofar)
		}
	}
}

// startLive returns the live decoder for the session's model: a stream for a
// streaming model, chunks at pauses for an offline one with
// long_form_chunking, or nil to decode the recording whole once it stops.
func (c *Coordinator) startLive(s *session) liveDecoder {
	o := liveOptions{
		engine: c.eng,
		ctx:    s.ctx,
		opts:   s.options(),
		rate:   s.rate,
		gate:   GateFromSettings(s.settings),
		log:    c.log.With("session", s.id),
	}
	switch {
	case c.info.Streaming:
		c.log.Debug("transcribing live through a stream", "session", s.id)
		return startStreamDecoder(o)
	case s.settings.LongFormChunking:
		c.log.Debug("transcribing live in chunks cut at pauses", "session", s.id)
		return newChunkDecoder(o, defaultChunkPlan)
	default:
		return nil
	}
}

// publishInterim publishes the transcript so far while session s records.
func (c *Coordinator) publishInterim(s *session, in interim) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess == s && c.state == StateRecording {
		c.emitLocked(Event{Kind: EventInterim, Session: s.id, State: StateRecording, Text: in.text, Stable: in.stable})
	}
}

// discardLive releases the live decoder of a session that ended without a
// transcript. It runs after the pump, which still owns the decoder.
func (c *Coordinator) discardLive(s *session) {
	c.jobs.post(func() { closeLive(s) })
}

func closeLive(s *session) {
	if s.live != nil {
		s.live.close()
		s.live = nil
	}
}

// transcribe finishes decoding the recording: the live decoder's final, or a
// decode of the whole recording. It runs on the engine goroutine, after the
// pump.
func (c *Coordinator) transcribe(s *session, samples []float32, rate int) {
	defer closeLive(s)
	if s.ctx.Err() != nil {
		return
	}
	var res engine.Result
	err := s.prepErr
	if err == nil {
		start := time.Now()
		if s.live != nil {
			res, err = s.live.finish(s.ctx, samples)
		} else {
			res, err = c.eng.Recognize(s.ctx, samples, rate, s.options())
		}
		s.timings.transcribe = time.Since(start)
		if err == nil {
			err = CheckTranscript(res)
		}
	}
	if s.ctx.Err() != nil {
		c.log.Debug("transcript discarded: the session was cancelled", "session", s.id)
		return
	}
	if err != nil {
		c.fail(s, err)
		return
	}
	c.stageDone(s, "transcribe", s.timings.transcribe)

	c.mu.Lock()
	if c.sess != s {
		c.mu.Unlock()
		c.log.Debug("transcript discarded: the session was cancelled", "session", s.id)
		return
	}
	c.setStateLocked(StatePostProcessing, s.id)
	c.mu.Unlock()
	c.tasks.Go(func() { c.finish(s, res, samples, rate) })
}

// fail ends a session whose recording could not be transcribed, or held no
// speech, which is not a failure and raises no notice.
func (c *Coordinator) fail(s *session, err error) {
	c.mu.Lock()
	current := c.sess == s
	if current {
		if errors.Is(err, ErrNoSpeech) {
			c.log.Debug("dictation ended without speech", "session", s.id, "reason", err)
		} else {
			// Raised before going idle, so whoever follows the state
			// finds the notice already there.
			c.log.Warn("dictation failed", "session", s.id, "err", err)
			c.notices.Publish(c.failureNotice(s, err))
		}
		c.endLocked(s)
	}
	c.mu.Unlock()
	s.end()
}

func (c *Coordinator) failureNotice(s *session, err error) notice.Notice {
	if _, ok := errors.AsType[*loadError](err); ok {
		switch {
		case errors.Is(err, engine.ErrModelNotFound):
			return notice.NoModel()
		case errors.Is(err, engine.ErrRuntime), errors.Is(err, engine.ErrInvalidArgument):
			// A model the engine cannot read: a damaged or wrong file.
			return notice.ModelCorrupt(displayName(s.model))
		}
	}
	return notice.TranscriptionFailed(err)
}

// finish runs the text stages, delivers the transcript and saves the
// session to history, on its own goroutine.
func (c *Coordinator) finish(s *session, res engine.Result, samples []float32, rate int) {
	info := SessionInfo{
		Session:   s.id,
		Model:     s.model.Spec.ID,
		Language:  s.model.Language,
		Languages: slices.Clone(res.Languages),
		Duration:  s.timings.recording,
	}
	text := res.Text
	for _, st := range []struct {
		name  string
		stage TextStage
		took  *time.Duration
	}{
		{"cleanup", c.stages.Cleanup, &s.timings.cleanup},
		{"llm", c.stages.LLM, &s.timings.llm},
		{"hook", c.stages.Hook, &s.timings.hook},
	} {
		if st.stage == nil {
			continue
		}
		if s.ctx.Err() != nil {
			return
		}
		start := time.Now()
		out, err := st.stage.Process(s.ctx, text, info)
		*st.took = time.Since(start)
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			c.log.Warn("a text stage failed; passing its input on", "session", s.id, "stage", st.name, "err", err)
		} else {
			text = out
		}
		c.stageDone(s, st.name, *st.took)
	}

	if !c.advance(s, StatePostProcessing, StateOutputting) {
		return
	}
	var outErr error
	delivered := false
	if strings.TrimSpace(text) != "" {
		start := time.Now()
		outErr = c.out.Deliver(s.ctx, text, output.OptionsFrom(s.settings))
		s.timings.output = time.Since(start)
		delivered = outErr == nil
		c.stageDone(s, "output", s.timings.output)
	} else {
		c.log.Debug("the text stages left nothing to paste", "session", s.id)
	}

	c.mu.Lock()
	current := c.sess == s
	if current {
		if delivered {
			c.emitLocked(Event{Kind: EventResult, Session: s.id, State: StateOutputting, Text: text, Languages: slices.Clone(res.Languages)})
		}
		if outErr != nil {
			c.log.Warn("the transcript could not be delivered", "session", s.id, "err", outErr)
			c.notices.Publish(notice.ForPasteError(outErr))
		}
		c.endLocked(s)
	}
	c.mu.Unlock()
	s.end()
	if !current {
		return
	}
	c.logFinished(s, len([]rune(text)), delivered, res.Languages)

	if c.stages.History != nil {
		entry := HistoryEntry{
			SessionInfo: info,
			At:          s.started,
			RawText:     res.Text,
			Text:        text,
			Samples:     samples,
			SampleRate:  rate,
			Delivered:   delivered,
		}
		if err := c.stages.History.Save(context.Background(), entry); err != nil {
			c.log.Warn("the dictation could not be saved to history", "session", s.id, "err", err)
		}
	}
}

// advance moves session s from one state to the next, unless it was
// cancelled in the meantime.
func (c *Coordinator) advance(s *session, from, to State) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess != s || c.state != from {
		return false
	}
	c.setStateLocked(to, s.id)
	return true
}

func (c *Coordinator) logFinished(s *session, chars int, delivered bool, detected []string) {
	t := s.timings
	c.log.Info("dictation finished", "session", s.id, "delivered", delivered, "chars", chars,
		"language", s.model.Language, "detected", detected,
		"total", time.Since(s.started).Round(time.Millisecond))
	c.log.Debug("dictation timings", "session", s.id,
		"recording", round(t.recording), "load", round(t.load), "transcribe", round(t.transcribe),
		"cleanup", round(t.cleanup), "llm", round(t.llm), "hook", round(t.hook), "output", round(t.output),
		"after_recording", round(time.Since(s.started)-t.recording))
}

func (c *Coordinator) stageDone(s *session, stage string, took time.Duration) {
	c.log.Debug("dictation stage done", "session", s.id, "stage", stage, "took", round(took))
}

// forwardLevels publishes the microphone's levels while recording.
func (c *Coordinator) forwardLevels(levels <-chan audio.Level) {
	for {
		select {
		case <-c.done:
			return
		case l := <-levels:
			c.mu.Lock()
			if c.state == StateRecording && c.sess != nil {
				c.emitLocked(Event{Kind: EventLevel, Session: c.sess.id, State: StateRecording, Level: l})
			}
			c.mu.Unlock()
		}
	}
}

// refuse publishes that action a was refused, leaving the state as it is.
func (c *Coordinator) refuse(a Action, why string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refuseLocked(a, why)
}

func (c *Coordinator) refuseLocked(a Action, why string) {
	var id uint64
	if c.sess != nil {
		id = c.sess.id
	}
	c.log.Debug("dictation action refused", "action", a.String(), "state", c.state.String(), "why", why)
	c.emitLocked(Event{Kind: EventRefused, Session: id, State: c.state, Action: a})
}

// endLocked returns to idle from session s, which must be the current one.
func (c *Coordinator) endLocked(s *session) {
	c.sess = nil
	c.setStateLocked(StateIdle, s.id)
}

func (c *Coordinator) setStateLocked(st State, id uint64) {
	c.state = st
	c.emitLocked(Event{Kind: EventState, Session: id, State: st})
}

func (c *Coordinator) emitLocked(ev Event) {
	for s := range c.subs {
		s.push(ev)
	}
}

// options are the session's recognition options.
func (s *session) options() engine.Options {
	return engine.Options{Language: s.model.Language}
}

// hold keeps the engine acquired until the session ends.
func (s *session) hold(release func()) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		release()
		return
	}
	s.release = release
	s.mu.Unlock()
}

// end cancels whatever the session still has running and lets go of the
// engine. It is safe to call more than once.
func (s *session) end() {
	s.mu.Lock()
	s.ended = true
	release := s.release
	s.release = nil
	s.mu.Unlock()
	s.cancel()
	if release != nil {
		release()
	}
}

// loadError is a model that could not be made ready for a session.
type loadError struct{ err error }

func (e *loadError) Error() string { return "loading the model: " + e.err.Error() }

func (e *loadError) Unwrap() error { return e.err }

func displayName(m Model) string {
	if m.Name != "" {
		return m.Name
	}
	if m.Spec.ID != "" {
		return m.Spec.ID
	}
	return "speech"
}

func round(d time.Duration) time.Duration { return d.Round(time.Millisecond) }
