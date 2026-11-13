package app

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/fake"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/output"
	"github.com/opragmatico/goscriber/internal/pipeline"
	"github.com/opragmatico/goscriber/internal/ui"
)

// Every app test dictates on fakes, so none opens a microphone or pastes.
func init() {
	newServices = func(*Runtime, Options, *notice.Hub) services {
		return services{
			recorder: newTestRecorder(),
			engine:   fake.New(fake.Script{Replies: []fake.Reply{fake.Transcript("hello from the test")}}),
			output:   &testOutput{},
			models:   testModels{},
		}
	}
}

// testRecorder records a second of a tone each time.
type testRecorder struct {
	levels chan audio.Level

	mu        sync.Mutex
	recording bool
	starts    int
	opens     int
	device    string
	alwaysOn  bool
	closed    bool
}

func newTestRecorder() *testRecorder { return &testRecorder{levels: make(chan audio.Level)} }

func (r *testRecorder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recording = true
	r.starts++
	return nil
}

func (r *testRecorder) Stop() []float32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.recording {
		return nil
	}
	r.recording = false
	p := make([]float32, 16000)
	for i := range p {
		p[i] = float32(0.1 * math.Sin(2*math.Pi*220*float64(i)/16000))
	}
	return p
}

func (r *testRecorder) SampleRate() int            { return 16000 }
func (r *testRecorder) Levels() <-chan audio.Level { return r.levels }

func (r *testRecorder) Open() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opens++
	return nil
}

func (r *testRecorder) SetDevice(d string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.device = d
}

func (r *testRecorder) SetAlwaysOn(on bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alwaysOn = on
	return nil
}

func (r *testRecorder) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
}

// testOutput records deliveries.
type testOutput struct {
	mu    sync.Mutex
	texts []string
	err   error
}

func (o *testOutput) Deliver(_ context.Context, text string, _ output.Options) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	o.texts = append(o.texts, text)
	return nil
}

func (o *testOutput) delivered() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.texts)
}

// testModels has whatever model is selected.
type testModels struct{}

func (testModels) Resolve(s config.Settings) (pipeline.Model, error) {
	if s.SelectedModel == "" {
		return pipeline.Model{}, pipeline.ErrNoModel
	}
	return pipeline.Model{Spec: engine.LoadSpec{ID: s.SelectedModel, Path: "/models/" + s.SelectedModel + ".gguf"}}, nil
}

// waitUI runs UI updates until cond holds.
func (q uiQueue) waitUI(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !cond() {
		select {
		case f := <-q:
			f()
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// drain runs the UI updates that arrive until none has for a while.
func (q uiQueue) drain() {
	for {
		select {
		case f := <-q:
			f()
		case <-time.After(100 * time.Millisecond):
			return
		}
	}
}

// The tray starts and stops dictation; the icon follows the session and the
// transcript is delivered.
func TestTrayDrivesDictation(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	out := sh.svc.output.(*testOutput)

	item(t, a.menu, ui.LabelStartDictation).Action()
	q.waitUI(t, "the recording icon", func() bool { return a.trayIcon == ui.TrayIcon(ui.TrayRecording) })
	item(t, a.menu, ui.LabelStopDictation).Action()
	q.waitUI(t, "the transcript", func() bool { return len(out.delivered()) == 1 })
	q.waitUI(t, "the idle icon", func() bool { return a.trayIcon == ui.TrayIcon(ui.TrayIdle) })
	if got := out.delivered(); got[0] != "hello from the test" {
		t.Errorf("delivered %q", got)
	}

	// Cancel discards a recording.
	item(t, a.menu, ui.LabelStartDictation).Action()
	q.waitUI(t, "the recording icon", func() bool { return a.trayIcon == ui.TrayIcon(ui.TrayRecording) })
	item(t, a.menu, ui.LabelCancel).Action()
	q.waitUI(t, "the idle icon", func() bool { return a.trayIcon == ui.TrayIcon(ui.TrayIdle) })
	time.Sleep(50 * time.Millisecond)
	if got := out.delivered(); len(got) != 1 {
		t.Errorf("delivered %q after a cancel", got)
	}
}

// The shortcut drives the coordinator; Escape is registered only while it
// records.
func TestShortcutDrivesDictation(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m", "transcribe_shortcut": "ctrl+shift+d", "activation_mode": "toggle"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	testShortcuts.waitLog(t, "+ctrl+shift+d")
	out := sh.svc.output.(*testOutput)

	testShortcuts.press(t, "ctrl+shift+d")
	q.waitUI(t, "recording", func() bool { return sh.recording })
	testShortcuts.waitLog(t, "+ctrl+shift+d", "+escape")
	testShortcuts.down(t, "escape")
	q.waitUI(t, "idle", func() bool { return a.trayIcon == ui.TrayIcon(ui.TrayIdle) && !sh.recording })
	testShortcuts.waitLog(t, "+ctrl+shift+d", "+escape", "-escape")

	testShortcuts.press(t, "ctrl+shift+d")
	q.waitUI(t, "recording", func() bool { return sh.recording })
	testShortcuts.press(t, "ctrl+shift+d")
	q.waitUI(t, "the transcript", func() bool { return len(out.delivered()) == 1 })
}

// A press with no model downloaded records nothing and raises the notice
// again; the shortcut learns that nothing started, so the next press tries
// again instead of stopping a recording that never began.
func TestShortcutWithoutAModel(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	writeSettings(t, home, `{"schema_version": 1, "transcribe_shortcut": "ctrl+shift+d"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	testShortcuts.waitLog(t, "+ctrl+shift+d")
	q.runNext(t) // the launch notice
	rec := sh.svc.recorder.(*testRecorder)

	for range 2 {
		before := len(a.notifications)
		testShortcuts.press(t, "ctrl+shift+d")
		q.waitUI(t, "the notice again", func() bool { return len(a.notifications) > before })
		q.drain() // the refusal, which resets the shortcut
		if sh.recording {
			t.Error("the shortcut should know nothing is recording")
		}
	}
	if n := len(a.notifications); n != 3 || a.notifications[n-1].Title != "No model downloaded" {
		t.Errorf("notifications %+v, want the launch one and one per press", a.notifications)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.starts != 0 {
		t.Errorf("%d recordings started without a model", rec.starts)
	}
}

func TestDictationFollowsSettingsAndStops(t *testing.T) {
	isolate(t)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	rec := sh.svc.recorder.(*testRecorder)
	if err := sh.rt.Settings.Update(func(s *config.Settings) {
		s.Microphone, s.AlwaysOnMicrophone = "USB Headset", true
	}); err != nil {
		t.Fatal(err)
	}
	q.runNext(t)
	rec.mu.Lock()
	device, alwaysOn := rec.device, rec.alwaysOn
	rec.mu.Unlock()
	if device != "USB Headset" || !alwaysOn {
		t.Errorf("recorder device %q, always on %v", device, alwaysOn)
	}

	sh.stop()
	rec.mu.Lock()
	closed := rec.closed
	rec.mu.Unlock()
	if !closed {
		t.Error("stopping the app should close the microphone")
	}
	if sh.dictation.State() != pipeline.StateIdle {
		t.Error("dictation should be idle once stopped")
	}
}

func TestNoEngineSaysWhy(t *testing.T) {
	_, err := noEngine{}.Load(context.Background(), engine.LoadSpec{})
	if !errors.Is(err, engine.ErrUnavailable) {
		t.Fatalf("err %v", err)
	}
	n := notice.TranscriptionFailed(err)
	if n.Message != "Nothing was pasted: the speech engine failed to load (unavailable: this build of Goscriber has no speech engine; build it with 'make deps build'). Try again." {
		t.Errorf("notice %q", n.Message)
	}
}

// The overlay shows while recording and transcribing, gets the levels, and
// its cancel button cancels.
func TestOverlayFollowsDictation(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m", "overlay_style": "minimal"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	rec := sh.svc.recorder.(*testRecorder)

	sh.dictate(pipeline.ActionStart)
	q.waitUI(t, "the overlay", func() bool { return sh.overlay.State() == ui.OverlayRecording && sh.overlay.Visible() })
	rec.levels <- audio.Level{RMS: 1}
	q.waitUI(t, "the level", func() bool { return slices.Contains(sh.overlay.Levels(), 1) })
	q.waitUI(t, "a clock tick", func() bool { return sh.overlay.Elapsed() == "0:00" })

	test.Tap(cancelButton(t, sh.overlay))
	q.waitUI(t, "the overlay to hide", func() bool { return !sh.overlay.Visible() && sh.overlay.State() == ui.OverlayHidden })
	if sh.dictation.State() != pipeline.StateIdle {
		t.Errorf("state %v after the overlay's cancel", sh.dictation.State())
	}

	// The none style keeps it off screen; the setting applies at once.
	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.OverlayStyle = config.OverlayNone }); err != nil {
		t.Fatal(err)
	}
	q.runNext(t)
	sh.dictate(pipeline.ActionStart)
	q.waitUI(t, "recording", func() bool { return sh.recording })
	if sh.overlay.Visible() {
		t.Error("the none style should keep the overlay hidden")
	}
}

// cancelButton finds the overlay's cancel button among its window's objects.
func cancelButton(t *testing.T, o *ui.Overlay) fyne.Tappable {
	t.Helper()
	var found fyne.Tappable
	var walk func(fyne.CanvasObject)
	walk = func(obj fyne.CanvasObject) {
		if tp, ok := obj.(fyne.Tappable); ok && found == nil {
			found = tp
		}
		if c, ok := obj.(*fyne.Container); ok {
			for _, child := range c.Objects {
				walk(child)
			}
		}
		if w, ok := obj.(fyne.Widget); ok && found == nil {
			for _, child := range test.WidgetRenderer(w).Objects() {
				walk(child)
			}
		}
	}
	walk(o.Window().Content())
	if found == nil {
		t.Fatal("no cancel button in the overlay")
	}
	return found
}

func TestLatestKeepsOnlyTheNewest(t *testing.T) {
	q := make(uiQueue, 8)
	l := &latest{do: q.do}
	var got []int
	for i := range 5 {
		l.post(func() { got = append(got, i) })
	}
	if len(q) != 1 {
		t.Fatalf("%d updates queued, want 1", len(q))
	}
	(<-q)()
	if !slices.Equal(got, []int{4}) {
		t.Errorf("ran %v, want only the newest", got)
	}

	// Within the interval after a run, updates are dropped.
	l.every = time.Hour
	l.post(func() { got = append(got, 5) })
	if len(q) != 0 {
		t.Errorf("%d updates queued within the interval", len(q))
	}
}
