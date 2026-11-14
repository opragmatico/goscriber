package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/autounload"
	"github.com/opragmatico/goscriber/internal/engine/fake"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/output"
)

// fakeRecorder hands back the samples it was given for every recording. Its
// Frames deliver them all, in 10 ms chunks, as soon as the recording starts.
type fakeRecorder struct {
	levels chan audio.Level

	mu        sync.Mutex
	samples   []float32
	rate      int
	startErr  error
	recording bool
	frames    chan []float32 // of the recording in progress
	starts    int
	stops     int
	trace     *tracer // records starts, stops and pre-roll drops, if set
}

// tracer records what the recorder and the feedback did, in order.
type tracer struct {
	mu     sync.Mutex
	events []string
}

func (tr *tracer) add(e string) {
	if tr == nil {
		return
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.events = append(tr.events, e)
}

func (tr *tracer) get() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return slices.Clone(tr.events)
}

func (r *fakeRecorder) DropPreRoll() { r.trace.add("drop pre-roll") }

func newFakeRecorder(samples []float32) *fakeRecorder {
	return &fakeRecorder{levels: make(chan audio.Level, 1), samples: samples, rate: rate}
}

func (r *fakeRecorder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trace.add("record")
	if r.startErr != nil {
		return r.startErr
	}
	r.recording = true
	r.starts++
	n := max(1, r.rate/100)
	r.frames = make(chan []float32, len(r.samples)/n+1)
	for off := 0; off < len(r.samples); off += n {
		r.frames <- slices.Clone(r.samples[off:min(off+n, len(r.samples))])
	}
	return nil
}

func (r *fakeRecorder) Stop() []float32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.recording {
		return nil
	}
	r.trace.add("stop recording")
	r.recording = false
	r.stops++
	close(r.frames)
	return slices.Clone(r.samples)
}

func (r *fakeRecorder) Frames() <-chan []float32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.recording {
		ch := make(chan []float32)
		close(ch)
		return ch
	}
	return r.frames
}

func (r *fakeRecorder) SampleRate() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rate
}

func (r *fakeRecorder) Levels() <-chan audio.Level { return r.levels }

func (r *fakeRecorder) counts() (starts, stops int, recording bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts, r.stops, r.recording
}

// fakeOutput records deliveries.
type fakeOutput struct {
	mu    sync.Mutex
	texts []string
	opts  []output.Options
	err   error
	delay time.Duration
}

func (o *fakeOutput) Deliver(ctx context.Context, text string, opts output.Options) error {
	o.mu.Lock()
	delay, err := o.delay, o.err
	o.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return &output.Error{Op: "paste", Err: ctx.Err()}
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err != nil {
		return err
	}
	o.texts = append(o.texts, text)
	o.opts = append(o.opts, opts)
	return nil
}

func (o *fakeOutput) delivered() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.texts)
}

// fakeNotices records what is published and resolved.
type fakeNotices struct {
	mu        sync.Mutex
	published []notice.Notice
	resolved  []notice.Code
}

func (n *fakeNotices) Publish(x notice.Notice) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.published = append(n.published, x)
	return true
}

func (n *fakeNotices) Resolve(code notice.Code) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.resolved = append(n.resolved, code)
	return true
}

func (n *fakeNotices) codes() []notice.Code {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []notice.Code
	for _, x := range n.published {
		out = append(out, x.Code)
	}
	return out
}

// fakeModels resolves to one model, or fails with err.
type fakeModels struct {
	mu    sync.Mutex
	model Model
	err   error
}

func (m *fakeModels) Resolve(config.Settings) (Model, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.model, m.err
}

var testModel = Model{
	Spec:     engine.LoadSpec{ID: "asr", Path: "/models/asr.gguf"},
	Name:     "Test ASR",
	Language: "en-US",
}

// rig is a coordinator over fakes, with every event it publishes.
type rig struct {
	c       *Coordinator
	rec     *fakeRecorder
	eng     *fake.Engine
	out     *fakeOutput
	notices *fakeNotices
	models  *fakeModels
	events  *eventLog
}

type rigOptions struct {
	script   fake.Script
	samples  []float32
	engine   engine.Engine // overrides the fake engine
	stages   Stages
	settings func(*config.Settings)
	feedback Feedback
	micOK    func() bool
}

func newRig(t *testing.T, o rigOptions) *rig {
	t.Helper()
	if o.samples == nil {
		o.samples = tone(time.Second, 0.1)
	}
	r := &rig{
		rec:     newFakeRecorder(o.samples),
		eng:     fake.New(o.script),
		out:     &fakeOutput{},
		notices: &fakeNotices{},
		models:  &fakeModels{model: testModel},
	}
	eng := engine.Engine(r.eng)
	if o.engine != nil {
		eng = o.engine
	}
	settings := config.Defaults()
	if o.settings != nil {
		o.settings(&settings)
	}
	r.c = New(Options{
		Recorder:         r.rec,
		Engine:           eng,
		Models:           r.models,
		Output:           r.out,
		Notices:          r.notices,
		Settings:         func() config.Settings { return settings },
		Feedback:         o.feedback,
		MicrophoneAccess: o.micOK,
		Stages:           o.stages,
		Log:              slog.New(slog.DiscardHandler),
	})
	r.events = watch(t, r.c)
	t.Cleanup(r.c.Close)
	return r
}

// eventLog collects a subscription's events.
type eventLog struct {
	mu     sync.Mutex
	events []Event
	wake   chan struct{}
}

func watch(t *testing.T, c *Coordinator) *eventLog {
	t.Helper()
	l := &eventLog{wake: make(chan struct{}, 1)}
	ch, stop := c.Subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range ch {
			l.mu.Lock()
			l.events = append(l.events, ev)
			l.mu.Unlock()
			signal(l.wake)
		}
	}()
	t.Cleanup(func() { stop(); <-done })
	return l
}

func (l *eventLog) all() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// states returns the states the coordinator went through.
func (l *eventLog) states() []State {
	var out []State
	for _, ev := range l.all() {
		if ev.Kind == EventState {
			out = append(out, ev.State)
		}
	}
	return out
}

func (l *eventLog) kinds(k EventKind) []Event {
	var out []Event
	for _, ev := range l.all() {
		if ev.Kind == k {
			out = append(out, ev)
		}
	}
	return out
}

// waitFor waits until an event matches.
func (l *eventLog) waitFor(t *testing.T, what string, match func(Event) bool) Event {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for seen := 0; ; {
		evs := l.all()
		for _, ev := range evs[seen:] {
			if match(ev) {
				return ev
			}
		}
		seen = len(evs)
		select {
		case <-l.wake:
		case <-deadline:
			t.Fatalf("no %s event; events %v", what, evs)
			return Event{}
		}
	}
}

// waitState waits for the nth (from 1) move to state st.
func (l *eventLog) waitState(t *testing.T, st State, nth int) {
	t.Helper()
	n := 0
	l.waitFor(t, "state "+st.String(), func(ev Event) bool {
		if ev.Kind == EventState && ev.State == st {
			n++
		}
		return n == nth
	})
}

func (l *eventLog) waitRefused(t *testing.T, a Action) Event {
	t.Helper()
	return l.waitFor(t, "refused "+a.String(), func(ev Event) bool { return ev.Kind == EventRefused && ev.Action == a })
}

func wantStates(t *testing.T, got []State, want ...State) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("states %v, want %v", got, want)
	}
}

func TestNormalFlow(t *testing.T) {
	r := newRig(t, rigOptions{script: fake.Script{Replies: []fake.Reply{fake.Transcript("hello world"), fake.Transcript("again")}}})

	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)

	wantStates(t, r.events.states(), StateRecording, StateTranscribing, StatePostProcessing, StateOutputting, StateIdle)
	if got := r.out.delivered(); !slices.Equal(got, []string{"hello world"}) {
		t.Errorf("delivered %q", got)
	}
	if o := r.out.opts[0]; o != output.OptionsFrom(config.Defaults()) {
		t.Errorf("output options %+v, want the settings'", o)
	}
	res := r.events.kinds(EventResult)
	if len(res) != 1 || res[0].Text != "hello world" || res[0].Session != 1 {
		t.Errorf("result events %+v", res)
	}
	if loads := r.eng.Loads(); len(loads) != 1 || loads[0] != testModel.Spec {
		t.Errorf("loads %+v, want the model's spec once", loads)
	}
	recs := r.eng.Recognitions()
	if len(recs) != 1 || recs[0].SampleRate != rate || recs[0].Samples != rate || recs[0].Options.Language != "en-US" {
		t.Errorf("recognitions %+v", recs)
	}
	if codes := r.notices.codes(); len(codes) != 0 {
		t.Errorf("notices %v", codes)
	}
	if r.c.State() != StateIdle {
		t.Errorf("state %v", r.c.State())
	}

	// The next session reuses the loaded model and gets a new ID.
	r.c.Toggle()
	r.events.waitState(t, StateRecording, 2)
	r.c.Toggle()
	r.events.waitState(t, StateIdle, 2)
	if got := r.out.delivered(); !slices.Equal(got, []string{"hello world", "again"}) {
		t.Errorf("delivered %q", got)
	}
	if loads := r.eng.Loads(); len(loads) != 1 {
		t.Errorf("%d loads, want the model kept loaded", len(loads))
	}
	if res := r.events.kinds(EventResult); len(res) != 2 || res[1].Session != 2 {
		t.Errorf("result events %+v", res)
	}
}

func TestCancelWhileRecording(t *testing.T) {
	r := newRig(t, rigOptions{script: fake.Script{Replies: []fake.Reply{fake.Transcript("unwanted")}}})

	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Cancel()
	r.events.waitState(t, StateIdle, 1)

	wantStates(t, r.events.states(), StateRecording, StateIdle)
	if _, stops, recording := r.rec.counts(); stops != 1 || recording {
		t.Errorf("recorder stopped %d times, recording %v", stops, recording)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(r.eng.Recognitions()); n != 0 {
		t.Errorf("%d recognitions of a cancelled recording", n)
	}
	if got := r.out.delivered(); len(got) != 0 {
		t.Errorf("delivered %q", got)
	}
	// A second cancel has nothing to cancel.
	r.c.Cancel()
	if ev := r.events.waitRefused(t, ActionCancel); ev.State != StateIdle {
		t.Errorf("refusal %+v", ev)
	}
}

func TestCancelWhileTranscribingDiscardsTheResult(t *testing.T) {
	slow := fake.Transcript("too late")
	slow.Delay = 300 * time.Millisecond
	// The engine finishes the decode it started even when cancelled, as
	// NeMo-Speech.cpp may: the result must still be dropped.
	r := newRig(t, rigOptions{script: fake.Script{Replies: []fake.Reply{slow, fake.Transcript("next one")}}})

	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateTranscribing, 1)
	for len(r.eng.Recognitions()) == 0 { // the decode has begun
		time.Sleep(time.Millisecond)
	}
	r.c.Cancel()
	r.events.waitState(t, StateIdle, 1)

	// A new session starts while the old decode is still unwinding.
	r.c.Start()
	r.events.waitState(t, StateRecording, 2)
	time.Sleep(400 * time.Millisecond)
	if got := r.out.delivered(); len(got) != 0 {
		t.Fatalf("delivered %q: the cancelled transcript must be dropped", got)
	}
	if st := r.c.State(); st != StateRecording {
		t.Fatalf("state %v: the old result must not move the new session on", st)
	}
	r.c.Stop()
	r.events.waitState(t, StateIdle, 2)
	if got := r.out.delivered(); !slices.Equal(got, []string{"next one"}) {
		t.Errorf("delivered %q", got)
	}
	wantStates(t, r.events.states(),
		StateRecording, StateTranscribing, StateIdle,
		StateRecording, StateTranscribing, StatePostProcessing, StateOutputting, StateIdle)
	if codes := r.notices.codes(); len(codes) != 0 {
		t.Errorf("a cancel is not a failure: notices %v", codes)
	}
}

func TestShortcutWhileTranscribingIsRefused(t *testing.T) {
	slow := fake.Transcript("finished")
	slow.Delay = 200 * time.Millisecond
	r := newRig(t, rigOptions{script: fake.Script{Replies: []fake.Reply{slow}}})

	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateTranscribing, 1)
	r.c.Start()
	r.c.Toggle()
	r.c.Stop()
	for _, a := range []Action{ActionStart, ActionToggle, ActionStop} {
		if ev := r.events.waitRefused(t, a); ev.State != StateTranscribing || ev.Session != 1 {
			t.Errorf("refusal of %v: %+v", a, ev)
		}
	}
	r.events.waitState(t, StateIdle, 1)
	if starts, _, _ := r.rec.counts(); starts != 1 {
		t.Errorf("%d recordings started, want 1", starts)
	}
	if got := r.out.delivered(); !slices.Equal(got, []string{"finished"}) {
		t.Errorf("delivered %q: the transcript should still come out, once", got)
	}
}

func TestEngineErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script fake.Script
		want   notice.Code
	}{
		{"decode fails", fake.Script{Replies: []fake.Reply{{Err: engine.ErrOutOfMemory}}}, notice.CodeTranscriptionFailed},
		{"model damaged", fake.Script{LoadErr: engine.ErrRuntime}, notice.CodeModelCorrupt},
		{"model gone", fake.Script{LoadErr: engine.ErrModelNotFound}, notice.CodeNoModel},
		{"engine unavailable", fake.Script{LoadErr: engine.ErrUnavailable}, notice.CodeTranscriptionFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, rigOptions{script: tc.script})
			r.c.Start()
			r.events.waitState(t, StateRecording, 1)
			r.c.Stop()
			r.events.waitState(t, StateIdle, 1)
			wantStates(t, r.events.states(), StateRecording, StateTranscribing, StateIdle)
			if codes := r.notices.codes(); !slices.Equal(codes, []notice.Code{tc.want}) {
				t.Errorf("notices %v, want %v", codes, tc.want)
			}
			if got := r.out.delivered(); len(got) != 0 {
				t.Errorf("delivered %q", got)
			}
		})
	}
}

func TestLoadFailureIsRetriedNextSession(t *testing.T) {
	inner := fake.New(fake.Script{Replies: []fake.Reply{fake.Transcript("ok")}})
	flaky := &failFirstLoad{Engine: inner}
	r := newRig(t, rigOptions{engine: flaky})
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)
	r.c.Start()
	r.events.waitState(t, StateRecording, 2)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 2)
	if got := r.out.delivered(); !slices.Equal(got, []string{"ok"}) {
		t.Errorf("delivered %q: the second session should load the model again", got)
	}
}

type failFirstLoad struct {
	*fake.Engine
	mu    sync.Mutex
	loads int
}

func (e *failFirstLoad) Load(ctx context.Context, spec engine.LoadSpec) (engine.ModelInfo, error) {
	e.mu.Lock()
	e.loads++
	first := e.loads == 1
	e.mu.Unlock()
	if first {
		return engine.ModelInfo{}, &engine.Error{Op: "load", Kind: engine.ErrOutOfMemory}
	}
	return e.Engine.Load(ctx, spec)
}

func TestNoModelRaisesTheNoticeAndRecordsNothing(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.models.err = ErrNoModel

	r.c.Start()
	r.c.Start()
	r.events.waitFor(t, "second refusal", func(Event) bool { return len(r.events.kinds(EventRefused)) == 2 })
	for _, ev := range r.events.kinds(EventRefused) {
		if ev.Action != ActionStart || ev.State != StateIdle || ev.Session != 0 {
			t.Errorf("refusal %+v", ev)
		}
	}
	// Every press raises the notice anew, even while it is active.
	r.notices.mu.Lock()
	resolved := slices.Clone(r.notices.resolved)
	r.notices.mu.Unlock()
	if want := []notice.Code{notice.CodeNoModel, notice.CodeNoModel}; !slices.Equal(resolved, want) {
		t.Errorf("resolved %v, want %v", resolved, want)
	}
	if codes := r.notices.codes(); !slices.Equal(codes, []notice.Code{notice.CodeNoModel, notice.CodeNoModel}) {
		t.Errorf("notices %v", codes)
	}
	if starts, _, _ := r.rec.counts(); starts != 0 {
		t.Errorf("%d recordings started without a model", starts)
	}
	if states := r.events.states(); len(states) != 0 {
		t.Errorf("states %v, want none", states)
	}
	if n := len(r.eng.Loads()); n != 0 {
		t.Errorf("%d loads", n)
	}
}

func TestCorruptModelRaisesItsNotice(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.models.model, r.models.err = Model{Name: "Parakeet"}, ErrModelCorrupt
	r.c.Start()
	r.events.waitRefused(t, ActionStart)
	r.notices.mu.Lock()
	defer r.notices.mu.Unlock()
	if len(r.notices.published) != 1 || r.notices.published[0].Code != notice.CodeModelCorrupt ||
		!strings.Contains(r.notices.published[0].Message, "Parakeet") {
		t.Errorf("notices %+v", r.notices.published)
	}
}

func TestMicrophoneFailure(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.rec.startErr = errors.New("device busy")
	r.c.Start()
	r.events.waitRefused(t, ActionStart)
	r.notices.mu.Lock()
	defer r.notices.mu.Unlock()
	if len(r.notices.published) != 1 || r.notices.published[0].Code != notice.CodeMicrophoneUnavailable ||
		!strings.Contains(r.notices.published[0].Message, "device busy") {
		t.Errorf("notices %+v", r.notices.published)
	}
}

func TestNoSpeechEndsQuietly(t *testing.T) {
	for _, tc := range []struct {
		name        string
		samples     []float32
		reply       fake.Reply
		recognizing bool
	}{
		{"too short", tone(100*time.Millisecond, 0.1), fake.Transcript("x"), false},
		{"silent", make([]float32, rate), fake.Transcript("x"), false},
		{"empty transcript", tone(time.Second, 0.1), fake.Transcript("  "), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, rigOptions{samples: tc.samples, script: fake.Script{Replies: []fake.Reply{tc.reply}}})
			r.c.Start()
			r.events.waitState(t, StateRecording, 1)
			r.c.Stop()
			r.events.waitState(t, StateIdle, 1)
			want := []State{StateRecording, StateIdle}
			if tc.recognizing {
				want = []State{StateRecording, StateTranscribing, StateIdle}
			}
			wantStates(t, r.events.states(), want...)
			if n := len(r.eng.Recognitions()); (n > 0) != tc.recognizing {
				t.Errorf("%d recognitions", n)
			}
			if codes := r.notices.codes(); len(codes) != 0 {
				t.Errorf("no speech is not a failure: notices %v", codes)
			}
			if got := r.out.delivered(); len(got) != 0 {
				t.Errorf("delivered %q", got)
			}
		})
	}
}

func TestOutputFailureRaisesANotice(t *testing.T) {
	r := newRig(t, rigOptions{script: fake.Script{Replies: []fake.Reply{fake.Transcript("text")}}})
	r.out.err = &output.Error{Op: "paste", Err: output.ErrNoPermission}
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)
	if codes := r.notices.codes(); !slices.Equal(codes, []notice.Code{notice.CodePermissionMissing}) {
		t.Errorf("notices %v", codes)
	}
	if res := r.events.kinds(EventResult); len(res) != 0 {
		t.Errorf("result events %+v for a failed delivery", res)
	}
}

func TestCancelWhileOutputting(t *testing.T) {
	r := newRig(t, rigOptions{script: fake.Script{Replies: []fake.Reply{fake.Transcript("text")}}})
	r.out.delay = time.Second
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateOutputting, 1)
	r.c.Cancel()
	r.events.waitState(t, StateIdle, 1)
	time.Sleep(50 * time.Millisecond)
	if codes := r.notices.codes(); len(codes) != 0 {
		t.Errorf("a cancelled delivery is not a failure: notices %v", codes)
	}
	if got := r.out.delivered(); len(got) != 0 {
		t.Errorf("delivered %q", got)
	}
}

func TestStages(t *testing.T) {
	var mu sync.Mutex
	var order []string
	var saved []HistoryEntry
	stage := func(name string, f func(string) (string, error)) TextStage {
		return TextStageFunc(func(_ context.Context, text string, info SessionInfo) (string, error) {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			if info.Session != 1 || info.Model != "asr" || info.Duration <= 0 {
				t.Errorf("%s: info %+v", name, info)
			}
			return f(text)
		})
	}
	r := newRig(t, rigOptions{
		script: fake.Script{Replies: []fake.Reply{{Result: engine.Result{Text: "um hello", Languages: []string{"en-US"}}}}},
		stages: Stages{
			Cleanup: stage("cleanup", func(s string) (string, error) { return strings.TrimPrefix(s, "um "), nil }),
			LLM:     stage("llm", func(string) (string, error) { return "", errors.New("provider down") }),
			Hook:    stage("hook", func(s string) (string, error) { return s + "!", nil }),
			History: HistorySinkFunc(func(_ context.Context, e HistoryEntry) error {
				mu.Lock()
				defer mu.Unlock()
				saved = append(saved, e)
				return nil
			}),
		},
	})
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)

	if got := r.out.delivered(); !slices.Equal(got, []string{"hello!"}) {
		t.Errorf("delivered %q, want the cleaned and hooked text with the failed LLM step skipped", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(saved)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(order, []string{"cleanup", "llm", "hook"}) {
		t.Errorf("stage order %v", order)
	}
	if len(saved) != 1 {
		t.Fatalf("%d history entries", len(saved))
	}
	e := saved[0]
	if e.RawText != "um hello" || e.Text != "hello!" || !e.Delivered || len(e.Samples) != rate ||
		e.SampleRate != rate || !slices.Equal(e.Languages, []string{"en-US"}) || e.Language != "en-US" || e.At.IsZero() {
		t.Errorf("history entry %+v", e)
	}
	if res := r.events.kinds(EventResult); len(res) != 1 || !slices.Equal(res[0].Languages, []string{"en-US"}) {
		t.Errorf("result events %+v, want the detected language", res)
	}
}

func TestEmptyStageOutputPastesNothing(t *testing.T) {
	r := newRig(t, rigOptions{
		script: fake.Script{Replies: []fake.Reply{fake.Transcript("text")}},
		stages: Stages{Hook: TextStageFunc(func(context.Context, string, SessionInfo) (string, error) { return "", nil })},
	})
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)
	if got := r.out.delivered(); len(got) != 0 {
		t.Errorf("delivered %q", got)
	}
	if res := r.events.kinds(EventResult); len(res) != 0 {
		t.Errorf("result events %+v", res)
	}
}

func TestLevelsOnlyWhileRecording(t *testing.T) {
	r := newRig(t, rigOptions{script: fake.Script{Replies: []fake.Reply{fake.Transcript("x")}}})
	r.rec.levels <- audio.Level{RMS: 0.9}
	time.Sleep(20 * time.Millisecond)
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.rec.levels <- audio.Level{RMS: 0.5, Peak: 0.7}
	ev := r.events.waitFor(t, "level", func(ev Event) bool { return ev.Kind == EventLevel })
	if ev.Level.RMS != 0.5 || ev.Session != 1 || ev.State != StateRecording {
		t.Errorf("level event %+v", ev)
	}
	if n := len(r.events.kinds(EventLevel)); n != 1 {
		t.Errorf("%d level events, want only the one while recording", n)
	}
}

func TestSubscriberGetsTheLatestLevel(t *testing.T) {
	s := newSubscriber()
	defer s.close()
	s.push(Event{Kind: EventState, State: StateRecording})
	for i := range 10 {
		s.push(Event{Kind: EventLevel, Level: audio.Level{RMS: float32(i)}})
	}
	s.push(Event{Kind: EventState, State: StateIdle})
	var got []Event
	for range 3 {
		got = append(got, <-s.out)
	}
	if got[0].State != StateRecording || got[1].Kind != EventLevel || got[1].Level.RMS != 9 || got[2].State != StateIdle {
		t.Errorf("events %+v, want the state, the latest level and the next state", got)
	}
}

// The model stays loaded from the start of a recording until the transcript
// is out, even with an unload timeout of 0, and is unloaded once idle.
func TestTheModelIsHeldDuringASession(t *testing.T) {
	inner := fake.New(fake.Script{Replies: []fake.Reply{fake.Transcript("x")}})
	auto := autounload.New(inner, 0, slog.New(slog.DiscardHandler))
	r := newRig(t, rigOptions{engine: auto})
	r.out.delay = 100 * time.Millisecond

	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	time.Sleep(50 * time.Millisecond)
	if !auto.Resident() {
		t.Error("the model should be loaded while recording")
	}
	r.c.Stop()
	r.events.waitState(t, StateOutputting, 1)
	if !auto.Resident() {
		t.Error("the model should stay loaded until the transcript is out")
	}
	r.events.waitState(t, StateIdle, 1)
	deadline := time.Now().Add(2 * time.Second)
	for auto.Resident() {
		if time.Now().After(deadline) {
			t.Fatal("the idle model was not unloaded")
		}
		time.Sleep(time.Millisecond)
	}
	// The next session loads it again through Acquire.
	r.c.Start()
	r.events.waitState(t, StateRecording, 2)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 2)
	if got := r.out.delivered(); len(got) != 2 {
		t.Errorf("delivered %q", got)
	}
	// At least one load per session: with a timeout of 0 the model may also
	// go between the first load and the coordinator's Acquire.
	if n := len(inner.Loads()); n < 2 {
		t.Errorf("%d loads, want the model loaded again for the second session", n)
	}
}

func TestANewModelIsLoaded(t *testing.T) {
	r := newRig(t, rigOptions{script: fake.Script{Replies: []fake.Reply{fake.Transcript("x")}}})
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)

	other := testModel
	other.Spec.ID, other.Spec.Path = "other", "/models/other.gguf"
	r.models.mu.Lock()
	r.models.model = other
	r.models.mu.Unlock()
	r.c.Start()
	r.events.waitState(t, StateRecording, 2)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 2)
	loads := r.eng.Loads()
	if len(loads) != 2 || loads[1] != other.Spec {
		t.Errorf("loads %+v", loads)
	}
}

func TestCloseDiscardsTheSession(t *testing.T) {
	r := newRig(t, rigOptions{})
	ch, stop := r.c.Subscribe()
	defer stop()
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Close()
	if _, stops, recording := r.rec.counts(); stops != 1 || recording {
		t.Errorf("recorder stopped %d times, recording %v", stops, recording)
	}
	for range ch { //nolint:revive // drain until Close ends the subscription
	}
	r.c.Start() // does nothing
	time.Sleep(20 * time.Millisecond)
	if starts, _, _ := r.rec.counts(); starts != 1 {
		t.Errorf("%d starts after Close", starts)
	}
	if r.c.State() != StateIdle {
		t.Errorf("state %v", r.c.State())
	}
}

// fakeFeedback traces the cues and the mute.
type fakeFeedback struct {
	trace *tracer
	opts  []audio.FeedbackOptions
	mu    sync.Mutex
}

func (f *fakeFeedback) Start(o audio.FeedbackOptions) bool {
	f.mu.Lock()
	f.opts = append(f.opts, o)
	f.mu.Unlock()
	f.trace.add("start cue")
	return o.Sounds
}

func (f *fakeFeedback) Stop(_ audio.FeedbackOptions, cancelled bool) {
	if cancelled {
		f.trace.add("unmute")
	} else {
		f.trace.add("unmute, stop cue")
	}
}

func TestFeedbackAroundRecording(t *testing.T) {
	tr := &tracer{}
	fb := &fakeFeedback{trace: tr}
	r := newRig(t, rigOptions{
		script:   fake.Script{Replies: []fake.Reply{fake.Transcript("x")}},
		feedback: fb,
		settings: func(s *config.Settings) {
			s.AudioFeedback, s.AudioFeedbackVolume, s.OutputDevice, s.MuteWhileRecording = true, 0.4, "Speakers", true
		},
	})
	r.rec.trace = tr

	// The start cue plays before capture, its echo leaves the pre-roll, and
	// the stop cue plays once capture has stopped.
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)
	want := []string{"start cue", "drop pre-roll", "record", "stop recording", "unmute, stop cue"}
	if got := tr.get(); !slices.Equal(got, want) {
		t.Errorf("trace %v, want %v", got, want)
	}
	if o := fb.opts[0]; o != (audio.FeedbackOptions{Sounds: true, Volume: 0.4, Device: "Speakers", Mute: true}) {
		t.Errorf("feedback options %+v, want the settings'", o)
	}

	// A cancel, a microphone that fails and quitting all unmute without the
	// stop cue.
	r.c.Start()
	r.events.waitState(t, StateRecording, 2)
	r.c.Cancel()
	r.events.waitState(t, StateIdle, 2)
	r.rec.mu.Lock()
	r.rec.startErr = errors.New("busy")
	r.rec.mu.Unlock()
	r.c.Start()
	r.events.waitRefused(t, ActionStart)
	r.rec.mu.Lock()
	r.rec.startErr = nil
	r.rec.mu.Unlock()
	r.c.Start()
	r.events.waitState(t, StateRecording, 3)
	r.c.Close()
	want = append(want,
		"start cue", "drop pre-roll", "record", "stop recording", "unmute",
		"start cue", "drop pre-roll", "record", "unmute",
		"start cue", "drop pre-roll", "record", "stop recording", "unmute")
	if got := tr.get(); !slices.Equal(got, want) {
		t.Errorf("trace\n%v, want\n%v", got, want)
	}
}

func TestNoFeedbackWithoutAModel(t *testing.T) {
	tr := &tracer{}
	r := newRig(t, rigOptions{feedback: &fakeFeedback{trace: tr}})
	r.models.err = ErrNoModel
	r.c.Start()
	r.events.waitRefused(t, ActionStart)
	if got := tr.get(); len(got) != 0 {
		t.Errorf("trace %v: no cue without a model", got)
	}
}

func TestNoMicrophonePermissionRecordsNothing(t *testing.T) {
	asked := 0
	tr := &tracer{}
	r := newRig(t, rigOptions{feedback: &fakeFeedback{trace: tr}, micOK: func() bool { asked++; return false }})
	r.c.Start()
	r.events.waitRefused(t, ActionStart)
	if codes := r.notices.codes(); !slices.Equal(codes, []notice.Code{notice.CodeMicrophoneDenied}) {
		t.Errorf("notices %v", codes)
	}
	if starts, _, _ := r.rec.counts(); starts != 0 || asked != 1 {
		t.Errorf("%d recordings, asked %d times", starts, asked)
	}
	if got := tr.get(); len(got) != 0 {
		t.Errorf("trace %v: no cue without the microphone", got)
	}
}
