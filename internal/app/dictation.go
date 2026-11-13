package app

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/autounload"
	"github.com/opragmatico/goscriber/internal/models"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/output"
	"github.com/opragmatico/goscriber/internal/pipeline"
	"github.com/opragmatico/goscriber/internal/platform"
	"github.com/opragmatico/goscriber/internal/ui"
)

// services are what dictation runs on: the microphone, the speech engine,
// the clipboard and keyboard, and the model files.
type services struct {
	recorder recorder
	engine   engine.Engine
	output   pipeline.Output
	models   pipeline.ModelSource
	feedback feedback
}

// feedback plays the cues and mutes other audio around recordings.
// *audio.Feedback implements it.
type feedback interface {
	pipeline.Feedback
	// Close puts other audio back and waits for a cue still playing.
	Close()
}

// recorder is the microphone as the shell drives it. *audio.Recorder
// implements it.
type recorder interface {
	pipeline.Recorder
	Open() error
	SetDevice(device string)
	SetAlwaysOn(on bool) error
	Close()
}

// newServices builds the services; tests replace it with fakes.
var newServices = systemServices

// systemServices are the real microphone, engine, clipboard and keyboard.
// Without a speech engine in this build, dictation records but every
// transcription fails with a notice that says so.
func systemServices(rt *Runtime, opts Options, notices *notice.Hub) services {
	s := rt.Settings.Get()
	rec := audio.NewRecorder(audio.Options{
		Device:   s.Microphone,
		AlwaysOn: s.AlwaysOnMicrophone,
		Notify:   func(msg string) { notices.Publish(notice.MicrophoneUnavailable(msg)) },
	})
	var inner engine.Engine = noEngine{}
	if opts.NewEngine != nil {
		inner = opts.NewEngine()
	} else {
		rt.Log.Warn("this build has no speech engine, so dictation cannot transcribe; build it with 'make deps build'")
	}
	return services{
		recorder: rec,
		engine:   autounload.New(inner, s.ModelUnloadTimeout.Duration(), rt.Log.Logger),
		output:   output.New(platform.GeneralPasteboard(), platform.Keyboard{}, rt.Log.Logger),
		models: pipeline.CatalogModels{Manager: models.NewManager(rt.Paths.ModelsDir(), models.Options{
			HFCache: models.HFCacheDir(os.Getenv),
		})},
		feedback: audio.NewFeedback(audio.NewPlayer(), platform.MuteOutput, rt.Log.Logger),
	}
}

// startDictation creates the coordinator and the overlay and follows the
// coordinator's state. It runs from start, on the main goroutine.
func (sh *shell) startDictation() {
	s := sh.rt.Settings.Get()
	sh.overlay = ui.NewOverlay(sh.app, ui.OverlayOptions{
		Style:    s.OverlayStyle,
		Position: s.OverlayPosition,
		OnCancel: func() { sh.dictate(pipeline.ActionCancel) },
	})
	sh.dictation = pipeline.New(pipeline.Options{
		Recorder:         sh.svc.recorder,
		Engine:           sh.svc.engine,
		Models:           sh.svc.models,
		Output:           sh.svc.output,
		Notices:          sh.notices,
		Settings:         sh.rt.Settings.Get,
		Feedback:         sh.svc.feedback,
		MicrophoneAccess: microphoneAccess,
		Log:              sh.rt.Log.Logger,
	})
	events, stop := sh.dictation.Subscribe()
	sh.stopDictationEvents = stop
	// The recorder measures audio.LevelRate (30) levels a second; the
	// throttle stops a burst after a stall from flooding the main thread,
	// with room for jitter so the steady rate gets through.
	levels := &latest{do: sh.do, every: time.Second / (audio.LevelRate + audio.LevelRate/3)}
	sh.following.Go(func() {
		for ev := range events {
			switch ev.Kind {
			case pipeline.EventLevel:
				levels.post(func() { sh.overlay.SetLevel(ev.Level) })
			case pipeline.EventState:
				sh.overlayActive.Store(ev.State != pipeline.StateIdle)
				fallthrough
			default:
				sh.do(func() { sh.dictationEvent(ev) })
			}
		}
	})
	sh.following.Go(sh.tickOverlay)
}

// overlayTick is how often the overlay's clock and, on macOS, its
// click-through follow the time and the mouse pointer while it is shown.
const overlayTick = 100 * time.Millisecond

// tickOverlay drives the overlay's clock while dictation is active, until
// the app stops. A tick that the main thread has not run yet is not doubled.
func (sh *shell) tickOverlay() {
	t := time.NewTicker(overlayTick)
	defer t.Stop()
	ticks := &latest{do: sh.do}
	for {
		select {
		case <-sh.quit:
			return
		case now := <-t.C:
			if sh.overlayActive.Load() {
				ticks.post(func() { sh.overlay.Tick(now) })
			}
		}
	}
}

// openMicrophone opens an always-on microphone at launch, so the first
// recording has its pre-roll. It runs in the background: opening a device
// can take a moment.
func (sh *shell) openMicrophone() {
	if !sh.rt.Settings.Get().AlwaysOnMicrophone {
		return
	}
	if err := sh.svc.recorder.Open(); err != nil {
		sh.rt.Log.Warn("the always-on microphone could not be opened", "err", err)
	}
}

// dictate carries out a dictation action from the shortcut or the tray. The
// coordinator's actions never block, so this is safe on any goroutine.
func (sh *shell) dictate(a pipeline.Action) {
	if sh.dictation != nil {
		sh.dictation.Do(a)
	}
}

// dictationEvent follows the coordinator, on the main thread: the tray icon
// shows the state, and the shortcuts learn whether a recording is in
// progress, so Escape is only taken while it is.
func (sh *shell) dictationEvent(ev pipeline.Event) {
	switch ev.Kind {
	case pipeline.EventState:
		if sh.tray != nil {
			sh.tray.SetState(trayState(ev.State))
		}
		sh.overlay.SetState(overlayState(ev.State), time.Now())
		sh.syncRecording(ev.State == pipeline.StateRecording, false)
	case pipeline.EventInterim:
		sh.overlay.SetInterim(ev.Text)
	case pipeline.EventRefused:
		// The shortcut assumed its press would start or stop a recording;
		// tell it what happened instead.
		sh.syncRecording(ev.State == pipeline.StateRecording, true)
	}
}

// syncRecording tells the shortcuts about a recording starting or ending,
// and, with force, the current state even when it has not changed.
func (sh *shell) syncRecording(on, force bool) {
	if on == sh.recording && !force {
		return
	}
	sh.recording = on
	sh.hotkeys.SetRecording(on)
}

// applyDictationSettings passes settings changes on to the services and
// the overlay.
func (sh *shell) applyDictationSettings(s config.Settings) {
	sh.overlay.SetStyle(s.OverlayStyle, s.OverlayPosition)
	sh.svc.recorder.SetDevice(s.Microphone)
	if err := sh.svc.recorder.SetAlwaysOn(s.AlwaysOnMicrophone); err != nil {
		sh.rt.Log.Warn("the always-on microphone could not be opened", "err", err)
	}
	if e, ok := sh.svc.engine.(interface{ SetTimeout(time.Duration) }); ok {
		e.SetTimeout(s.ModelUnloadTimeout.Duration())
	}
}

// stopDictation ends dictation and releases the microphone and the model.
// It runs from stop, after the app has quit.
func (sh *shell) stopDictation() {
	if sh.dictation != nil {
		sh.dictation.Close()
	}
	sh.svc.feedback.Close()
	if err := sh.svc.engine.Unload(); err != nil {
		sh.rt.Log.Warn("unloading the model failed", "err", err)
	}
	sh.svc.recorder.Close()
}

func overlayState(s pipeline.State) ui.OverlayState {
	switch s {
	case pipeline.StateIdle:
		return ui.OverlayHidden
	case pipeline.StateRecording:
		return ui.OverlayRecording
	default:
		return ui.OverlayTranscribing
	}
}

func trayState(s pipeline.State) ui.TrayState {
	switch s {
	case pipeline.StateIdle:
		return ui.TrayIdle
	case pipeline.StateRecording:
		return ui.TrayRecording
	default:
		return ui.TrayTranscribing
	}
}

// noEngine is the engine of a build without NeMo-Speech.cpp: loading any
// model fails and says why.
type noEngine struct{}

var errNoEngine = &engine.Error{
	Op:     "load",
	Kind:   engine.ErrUnavailable,
	Detail: "this build of Goscriber has no speech engine; build it with 'make deps build'",
}

func (noEngine) Load(context.Context, engine.LoadSpec) (engine.ModelInfo, error) {
	return engine.ModelInfo{}, errNoEngine
}

func (noEngine) Unload() error { return nil }

func (noEngine) Recognize(context.Context, []float32, int, engine.Options) (engine.Result, error) {
	return engine.Result{}, &engine.Error{Op: "recognize", Kind: engine.ErrNotLoaded}
}

func (noEngine) StartStream(context.Context, engine.Options) (engine.Stream, error) {
	return nil, &engine.Error{Op: "start stream", Kind: engine.ErrNotLoaded}
}

// latest posts UI updates that only matter in their newest form, such as
// the microphone level: at most one waits for the main thread at a time,
// and not more often than every.
type latest struct {
	do    func(func())
	every time.Duration

	mu      sync.Mutex
	f       func()
	pending bool
	last    time.Time
}

// post schedules f, replacing any update still waiting. An update that
// comes sooner than every after the last one is dropped.
func (l *latest) post(f func()) {
	l.mu.Lock()
	if l.pending || time.Since(l.last) < l.every {
		if l.pending {
			l.f = f
		}
		l.mu.Unlock()
		return
	}
	l.f, l.pending = f, true
	l.mu.Unlock()
	l.do(func() {
		l.mu.Lock()
		f := l.f
		l.f, l.pending, l.last = nil, false, time.Now()
		l.mu.Unlock()
		f()
	})
}
