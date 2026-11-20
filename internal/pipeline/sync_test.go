package pipeline

import (
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/engine"
	"github.com/opragmatico/goscriber/engine/fake"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine/autounload"
)

// flushJobs waits until the engine goroutine has run every job queued so far.
func (r *rig) flushJobs(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	if !r.c.jobs.post(func() { close(done) }) {
		t.Fatal("the engine goroutine is closed")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the engine goroutine is stuck")
	}
}

// selectModel makes the model source resolve to m, or fail with err.
func (r *rig) selectModel(m Model, err error) {
	r.models.mu.Lock()
	defer r.models.mu.Unlock()
	r.models.model, r.models.err = m, err
}

// waitModel waits for the nth (from 1) EventModel with load l.
func (l *eventLog) waitModel(t *testing.T, load ModelLoad, nth int) Event {
	t.Helper()
	n := 0
	return l.waitFor(t, "model "+load.String(), func(ev Event) bool {
		if ev.Kind == EventModel && ev.Load == load {
			n++
		}
		return n == nth
	})
}

func modelEvents(l *eventLog) []string {
	var out []string
	for _, ev := range l.kinds(EventModel) {
		out = append(out, fmt.Sprintf("%d %s %s", ev.Session, ev.Model, ev.Load))
	}
	return out
}

var otherModel = Model{
	Spec: engine.LoadSpec{ID: "asr", Path: "/models/asr.q4.gguf"},
	Name: "Test ASR (Q4)",
}

// With preload_model the selected model loads in the background, and the
// first recording finds it ready.
func TestSyncModelPreloads(t *testing.T) {
	r := newRig(t, rigOptions{script: fake.Script{Replies: []fake.Reply{fake.Transcript("ready")}}})

	r.c.SyncModel()
	r.events.waitModel(t, ModelReady, 1)
	if got, want := modelEvents(r.events), []string{"0 Test ASR loading", "0 Test ASR ready"}; !slices.Equal(got, want) {
		t.Errorf("model events %q, want %q", got, want)
	}
	if loads := r.eng.Loads(); len(loads) != 1 || loads[0] != testModel.Spec {
		t.Errorf("loads %+v, want the model's spec", loads)
	}

	// Again: the engine holds the model already.
	r.c.SyncModel()
	r.flushJobs(t)

	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)
	if got := r.out.delivered(); !slices.Equal(got, []string{"ready"}) {
		t.Errorf("delivered %q", got)
	}
	if n := len(r.eng.Loads()); n != 1 {
		t.Errorf("%d loads, want the preloaded model used", n)
	}
	if n := len(r.events.kinds(EventModel)); n != 2 {
		t.Errorf("%d model events, want none for the recording", n)
	}
}

// Without preloading, or with an unload timeout of 0, nothing loads before
// the first recording, which loads the model itself and says so.
func TestSyncModelWithoutPreloading(t *testing.T) {
	for name, set := range map[string]func(*config.Settings){
		"preload off":    func(s *config.Settings) { s.PreloadModel = false },
		"unload at once": func(s *config.Settings) { s.ModelUnloadTimeout = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, rigOptions{settings: set, script: fake.Script{Replies: []fake.Reply{fake.Transcript("lazy")}}})
			r.c.SyncModel()
			r.flushJobs(t)
			if n := len(r.eng.Loads()); n != 0 {
				t.Fatalf("%d loads before the first recording", n)
			}

			r.c.Start()
			r.events.waitState(t, StateRecording, 1)
			r.c.Stop()
			r.events.waitState(t, StateIdle, 1)
			if n := len(r.eng.Loads()); n != 1 {
				t.Errorf("%d loads, want the recording's", n)
			}
			if got, want := modelEvents(r.events), []string{"1 Test ASR loading", "1 Test ASR ready"}; !slices.Equal(got, want) {
				t.Errorf("model events %q, want %q", got, want)
			}
		})
	}
}

// Selecting another model, quantisation or accelerator loads it in the
// background, which unloads the old one.
func TestSyncModelSwitchesModels(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.c.SyncModel()
	r.events.waitModel(t, ModelReady, 1)

	r.selectModel(otherModel, nil)
	r.c.SyncModel()
	r.events.waitModel(t, ModelReady, 2)
	if loads := r.eng.Loads(); len(loads) != 2 || loads[1] != otherModel.Spec {
		t.Errorf("loads %+v, want the new model second", loads)
	}
	if n := r.eng.Unloads(); n != 1 {
		t.Errorf("%d unloads, want the old model unloaded", n)
	}
}

// Without preloading, a switch replaces a model held in memory, but not one
// unloaded for being idle: the next recording loads that.
func TestSyncModelWithoutPreloadingReplacesOnlyAResidentModel(t *testing.T) {
	inner := fake.New(fake.Script{Replies: []fake.Reply{fake.Transcript("one")}})
	idle := autounload.New(inner, autounload.Never, slog.New(slog.DiscardHandler))
	r := newRig(t, rigOptions{engine: idle, settings: func(s *config.Settings) { s.PreloadModel = false }})

	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)

	r.selectModel(otherModel, nil)
	r.c.SyncModel()
	r.events.waitFor(t, "the switch", func(ev Event) bool { return ev.Kind == EventModel && ev.Load == ModelReady && ev.Session == 0 })
	if loads := inner.Loads(); len(loads) != 2 || loads[1] != otherModel.Spec {
		t.Fatalf("loads %+v, want the new model loaded in the background", loads)
	}

	// Once idle unloaded, switching back loads nothing until it is needed.
	idle.SetTimeout(0)
	deadline := time.Now().Add(3 * time.Second)
	for idle.Resident() {
		if time.Now().After(deadline) {
			t.Fatal("the idle model was not unloaded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.selectModel(testModel, nil)
	r.c.SyncModel()
	r.flushJobs(t)
	if n := len(inner.Loads()); n != 2 {
		t.Errorf("%d loads, want none for an unloaded model", n)
	}
}

// When no model is ready, the model the engine holds is unloaded; a model
// selected again loads again.
func TestSyncModelUnloadsWhenNoModelIsReady(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.c.SyncModel()
	r.events.waitModel(t, ModelReady, 1)

	r.selectModel(Model{}, ErrNoModel)
	r.c.SyncModel()
	r.flushJobs(t)
	if r.eng.Loaded() || r.eng.Unloads() != 1 {
		t.Errorf("loaded %v after %d unloads; want the model unloaded", r.eng.Loaded(), r.eng.Unloads())
	}
	if codes := r.notices.codes(); len(codes) != 0 {
		t.Errorf("notices %v; nobody asked for dictation", codes)
	}

	r.selectModel(testModel, nil)
	r.c.SyncModel()
	r.events.waitModel(t, ModelReady, 2)
	if n := len(r.eng.Loads()); n != 2 {
		t.Errorf("%d loads, want the model loaded again", n)
	}
}

// A recording that starts while the model loads records at once, and is
// transcribed once the model is ready: it reaches the recording state before
// the load ends.
func TestRecordingWhileTheModelLoads(t *testing.T) {
	r := newRig(t, rigOptions{script: fake.Script{LoadDelay: 300 * time.Millisecond, Replies: []fake.Reply{fake.Transcript("patience")}}})

	r.c.SyncModel()
	r.events.waitModel(t, ModelLoading, 1)
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)

	if got := r.out.delivered(); !slices.Equal(got, []string{"patience"}) {
		t.Errorf("delivered %q", got)
	}
	if n := len(r.eng.Loads()); n != 1 {
		t.Errorf("%d loads, want the background one only", n)
	}
	var order []string
	for _, ev := range r.events.all() {
		switch {
		case ev.Kind == EventModel:
			order = append(order, ev.Load.String())
		case ev.Kind == EventState && ev.State == StateRecording:
			order = append(order, "recording")
		}
	}
	if want := []string{"loading", "recording", "ready"}; !slices.Equal(order, want) {
		t.Errorf("order %q, want %q", order, want)
	}
}

// A switch asked for during a session waits for the session to end: the
// session keeps its model until its transcript is out.
func TestSyncModelWaitsForTheSession(t *testing.T) {
	r := newRig(t, rigOptions{script: fake.Script{Replies: []fake.Reply{fake.Transcript("first model")}}})
	r.c.SyncModel()
	r.events.waitModel(t, ModelReady, 1)

	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.selectModel(otherModel, nil)
	r.c.SyncModel()
	r.c.Stop()
	r.events.waitModel(t, ModelReady, 2)

	if got := r.out.delivered(); !slices.Equal(got, []string{"first model"}) {
		t.Errorf("delivered %q", got)
	}
	var order []string
	for _, ev := range r.events.all() {
		switch {
		case ev.Kind == EventModel && ev.Load == ModelLoading:
			order = append(order, "load "+ev.Model)
		case ev.Kind == EventState && ev.State == StateIdle:
			order = append(order, "idle")
		}
	}
	if want := []string{"load Test ASR", "idle", "load Test ASR (Q4)"}; !slices.Equal(order, want) {
		t.Errorf("order %q, want %q", order, want)
	}
}

// A background load that fails raises no notice, and is not tried again in
// the background; the next recording tries again and says why it cannot
// transcribe.
func TestSyncModelFailureIsQuiet(t *testing.T) {
	r := newRig(t, rigOptions{script: fake.Script{LoadErr: engine.ErrModelNotFound}})
	r.c.SyncModel()
	r.events.waitModel(t, ModelFailed, 1)
	if codes := r.notices.codes(); len(codes) != 0 {
		t.Errorf("notices %v after a background load", codes)
	}
	r.c.SyncModel()
	r.flushJobs(t)
	if n := len(r.eng.Loads()); n != 1 {
		t.Errorf("%d loads, want the failed model left to the next recording", n)
	}

	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)
	if n := len(r.eng.Loads()); n != 2 {
		t.Errorf("%d loads, want the recording to try again", n)
	}
	if codes := r.notices.codes(); len(codes) != 1 {
		t.Errorf("notices %v, want the recording's", codes)
	}
}

// Close cancels a background load instead of waiting for it.
func TestCloseCancelsABackgroundLoad(t *testing.T) {
	r := newRig(t, rigOptions{script: fake.Script{LoadDelay: time.Minute}})
	r.c.SyncModel()
	r.events.waitModel(t, ModelLoading, 1)

	start := time.Now()
	r.c.Close()
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Close took %v", took)
	}
	if r.eng.Loaded() {
		t.Error("the cancelled load left a model loaded")
	}
	// After Close, SyncModel does nothing.
	r.c.SyncModel()
	if n := len(r.eng.Loads()); n != 1 {
		t.Errorf("%d loads", n)
	}
}

func TestModelLoadString(t *testing.T) {
	for l, want := range map[ModelLoad]string{ModelLoading: "loading", ModelReady: "ready", ModelFailed: "failed", 0: "none"} {
		if got := l.String(); got != want {
			t.Errorf("%d: %q, want %q", int(l), got, want)
		}
	}
	if EventModel.String() != "model" {
		t.Errorf("EventModel.String() = %q", EventModel.String())
	}
}
