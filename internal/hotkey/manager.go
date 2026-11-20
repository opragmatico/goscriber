package hotkey

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
)

// KeyEvent is a key going down or up.
type KeyEvent int

// Key events.
const (
	KeyDown KeyEvent = iota
	KeyUp
)

// Backend registers global shortcuts with the OS.
type Backend interface {
	// Register makes b the global shortcut for purpose p. The OS stops
	// delivering its key presses to the focused app while it is
	// registered. A backend whose keys belong to the desktop, such as the
	// XDG desktop portal's, goes by p and takes b as a suggestion at most.
	Register(p Purpose, b Binding) (Registration, error)
}

// Registration is a registered shortcut.
type Registration interface {
	// Events delivers the shortcut's key-downs and key-ups, in order, with
	// one key-down per press however long the key is held. It closes after
	// Unregister.
	Events() <-chan KeyEvent
	// Unregister gives the shortcut back to the OS.
	Unregister() error
}

// Config is what the Manager registers and how it reads the keys.
type Config struct {
	Transcribe string // the transcribe shortcut, as Parse reads it
	// PostProcess is the post-processing shortcut, which starts dictations
	// that are post-processed whatever llm_enabled says; "" registers none.
	PostProcess   string
	Cancel        string // the cancel shortcut, registered only while recording
	Mode          config.ActivationMode
	HoldThreshold time.Duration
}

// ConfigFrom returns the Config the settings describe.
func ConfigFrom(s config.Settings) Config {
	return Config{
		Transcribe:    s.TranscribeShortcut,
		PostProcess:   s.PostProcessShortcut,
		Cancel:        s.CancelShortcut,
		Mode:          s.ActivationMode,
		HoldThreshold: time.Duration(s.HoldThresholdMS) * time.Millisecond,
	}
}

// RegisterError is a shortcut that could not be registered, or is not a
// valid shortcut for its purpose.
type RegisterError struct {
	Purpose  Purpose
	Shortcut string
	Err      error
}

func (e *RegisterError) Error() string {
	return fmt.Sprintf("the %s shortcut %q is not available: %v", e.Purpose.Name(), e.Shortcut, e.Err)
}

func (e *RegisterError) Unwrap() error { return e.Err }

// Options configure a Manager.
type Options struct {
	// OnAction receives each action the shortcuts ask for, on the Manager's
	// goroutine. It must not block for long; hand the work over instead.
	OnAction func(Action)
	// OnError receives each *RegisterError, on the Manager's goroutine.
	OnError func(error)
	// OnRegistered is told each shortcut that registers, on the Manager's
	// goroutine, so a problem reported through OnError can be cleared.
	OnRegistered func(Purpose)
	// Log receives debug lines about registrations and actions. Nil means
	// slog's default logger.
	Log *slog.Logger
}

// Manager registers the transcribe, post-processing and cancel shortcuts
// and turns their key events into actions through an Activator.
//
// One goroutine owns the registrations and the Activator; the methods only
// queue work for it and never block. That matters on macOS, where
// registering a shortcut runs on the main thread: a caller on the main
// thread that waited for a registration would wait forever.
type Manager struct {
	be   Backend
	opts Options
	log  *slog.Logger
	now  func() time.Time

	mu    sync.Mutex
	queue []func()
	wake  chan struct{}
	done  chan struct{}

	// Owned by the run goroutine.
	cfg         Config
	applied     bool
	act         Activator
	recording   bool // dictation confirmed a recording in progress
	suspended   bool // the shortcuts are given back to the OS for now
	transcribe  slot
	postProcess slot
	cancel      slot
	closed      bool
}

// slot is one shortcut and its registration.
type slot struct {
	purpose  Purpose
	shortcut string       // as configured
	reg      Registration // nil when not registered
	events   <-chan KeyEvent
}

// NewManager returns a Manager that registers shortcuts through be. Nothing
// is registered until the first Apply.
func NewManager(be Backend, opts Options) *Manager {
	return newManager(be, opts, time.Now)
}

func newManager(be Backend, opts Options, now func() time.Time) *Manager {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{
		be:          be,
		opts:        opts,
		log:         log,
		now:         now,
		wake:        make(chan struct{}, 1),
		done:        make(chan struct{}),
		transcribe:  slot{purpose: PurposeTranscribe},
		postProcess: slot{purpose: PurposePostProcess},
		cancel:      slot{purpose: PurposeCancel},
	}
	go m.run()
	return m
}

// Apply registers the shortcuts cfg names, replacing those registered
// before, and switches to its activation mode. A shortcut that is
// unchanged and registered stays as it is; one that failed before is tried
// again. Failures go to OnError.
func (m *Manager) Apply(cfg Config) { m.post(func() { m.apply(cfg) }) }

// SetRecording tells the Manager whether dictation is recording. The cancel
// shortcut is registered while it is, so its key stays free otherwise.
// Report every change, including a refusal to start after ActionStart.
func (m *Manager) SetRecording(on bool) { m.post(func() { m.setRecording(on) }) }

// Suspend gives the shortcuts back to the OS while on, and registers them
// again once it is off. The settings window suspends them while it records
// a new shortcut, so pressing the current one reaches the window instead of
// starting dictation. Apply still takes effect, once the suspension ends.
func (m *Manager) Suspend(on bool) { m.post(func() { m.suspend(on) }) }

// Close unregisters the shortcuts and stops the Manager. It returns at
// once, so it is safe on the main thread; Done closes when it has finished.
func (m *Manager) Close() { m.post(m.close) }

// Done is closed once the Manager has stopped.
func (m *Manager) Done() <-chan struct{} { return m.done }

func (m *Manager) post(f func()) {
	m.mu.Lock()
	m.queue = append(m.queue, f)
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) run() {
	defer close(m.done)
	for !m.closed {
		select {
		case <-m.wake:
			m.mu.Lock()
			queue := m.queue
			m.queue = nil
			m.mu.Unlock()
			for _, f := range queue {
				if !m.closed {
					f()
				}
			}
		case ev, ok := <-m.transcribe.events:
			if !ok {
				m.transcribe.events = nil
				continue
			}
			m.key(PurposeTranscribe, ev)
		case ev, ok := <-m.postProcess.events:
			if !ok {
				m.postProcess.events = nil
				continue
			}
			m.key(PurposePostProcess, ev)
		case ev, ok := <-m.cancel.events:
			if !ok {
				m.cancel.events = nil
				continue
			}
			if ev == KeyDown {
				m.dispatch(m.act.Cancel())
			}
		}
	}
}

// key hands a key event of the transcribe or post-processing shortcut to
// the Activator.
func (m *Manager) key(p Purpose, ev KeyEvent) {
	if ev == KeyDown {
		m.dispatch(m.act.Press(p, m.now()))
	} else {
		m.dispatch(m.act.Release(p, m.now()))
	}
}

func (m *Manager) dispatch(a Action) {
	if a == ActionNone {
		return
	}
	m.log.Debug("shortcut action", "action", a.String(), "mode", string(m.act.Mode))
	if a == ActionStop || a == ActionCancel {
		// The recording is over as far as the shortcut knows: free the
		// cancel key at once rather than waiting for dictation to say so.
		m.recording = false
		m.syncCancel()
	}
	if m.opts.OnAction != nil {
		m.opts.OnAction(a)
	}
}

func (m *Manager) apply(cfg Config) {
	first := !m.applied
	m.applied = true
	m.act.Mode, m.act.HoldThreshold = cfg.Mode, cfg.HoldThreshold
	transcribeChanged := first || cfg.Transcribe != m.cfg.Transcribe || m.transcribe.reg == nil
	// The post-processing shortcut gives way to a transcribe shortcut
	// moved onto its keys, and the cancel shortcut to either.
	postChanged := first || cfg.PostProcess != m.cfg.PostProcess || m.postProcess.reg == nil ||
		(cfg.Transcribe != m.cfg.Transcribe && sameKeys(cfg.Transcribe, cfg.PostProcess))
	cancelChanged := first || cfg.Cancel != m.cfg.Cancel || cfg.Transcribe != m.cfg.Transcribe || cfg.PostProcess != m.cfg.PostProcess
	m.cfg = cfg

	// Every changed shortcut lets go of its keys before any registers, so
	// the two may swap keys.
	var changed []*slot
	if transcribeChanged {
		changed = append(changed, &m.transcribe)
	}
	if postChanged {
		changed = append(changed, &m.postProcess)
	}
	for _, s := range changed {
		m.unregister(s)
		// A key held during the switch belongs to the old shortcut.
		m.act.forget(s.purpose)
	}
	m.transcribe.shortcut, m.postProcess.shortcut = cfg.Transcribe, cfg.PostProcess
	if !m.suspended {
		for _, s := range changed {
			m.registerKey(s)
		}
	}
	if cancelChanged {
		m.unregister(&m.cancel)
		m.cancel.shortcut = cfg.Cancel
		m.syncCancel()
	}
}

// registerKey registers the transcribe or post-processing shortcut, unless
// the post-processing one is unset.
func (m *Manager) registerKey(s *slot) {
	if s.purpose == PurposePostProcess && s.shortcut == "" {
		return
	}
	if b, err := m.binding(s.purpose, s.shortcut); err == nil {
		m.register(s, b)
	}
}

// binding parses and validates a configured shortcut, reporting a problem
// to OnError.
func (m *Manager) binding(p Purpose, shortcut string) (Binding, error) {
	b, err := ParseFor(shortcut, p)
	if err == nil {
		err = m.clash(p, b)
	}
	if err != nil {
		m.fail(p, shortcut, err)
		return Binding{}, err
	}
	return b, nil
}

// clash reports a shortcut whose keys b, the shortcut for p, would take:
// the transcribe shortcut keeps its keys, and the cancel shortcut, only
// registered while recording, gives way to both others.
func (m *Manager) clash(p Purpose, b Binding) error {
	var others []Purpose
	switch p {
	case PurposePostProcess:
		others = []Purpose{PurposeTranscribe}
	case PurposeCancel:
		others = []Purpose{PurposeTranscribe, PurposePostProcess}
	}
	for _, o := range others {
		shortcut := m.cfg.Transcribe
		if o == PurposePostProcess {
			shortcut = m.cfg.PostProcess
		}
		if ob, err := Parse(shortcut); err == nil && ob == b {
			return fmt.Errorf("it is the %s shortcut too", o.Name())
		}
	}
	return nil
}

// sameKeys reports whether two configured shortcuts are the same keys.
func sameKeys(a, b string) bool {
	ab, aerr := Parse(a)
	bb, berr := Parse(b)
	return aerr == nil && berr == nil && ab == bb
}

func (m *Manager) suspend(on bool) {
	if on == m.suspended {
		return
	}
	m.suspended = on
	for _, s := range []*slot{&m.transcribe, &m.postProcess} {
		if on {
			m.unregister(s)
			m.act.forget(s.purpose)
		} else if m.applied {
			m.registerKey(s)
		}
	}
	m.syncCancel()
}

func (m *Manager) setRecording(on bool) {
	m.act.SetRecording(on)
	m.recording = on
	m.syncCancel()
}

// syncCancel registers the cancel shortcut while dictation is recording and
// the shortcuts are not suspended, and unregisters it otherwise.
func (m *Manager) syncCancel() {
	want := m.recording && m.applied && !m.suspended
	switch {
	case want && m.cancel.reg == nil:
		if b, err := m.binding(PurposeCancel, m.cancel.shortcut); err == nil {
			m.register(&m.cancel, b)
		}
	case !want && m.cancel.reg != nil:
		m.unregister(&m.cancel)
	}
}

func (m *Manager) register(s *slot, b Binding) {
	reg, err := m.be.Register(s.purpose, b)
	if err != nil {
		m.fail(s.purpose, s.shortcut, err)
		return
	}
	s.reg, s.events = reg, reg.Events()
	m.log.Debug("shortcut registered", "purpose", string(s.purpose), "shortcut", b.String())
	if m.opts.OnRegistered != nil {
		m.opts.OnRegistered(s.purpose)
	}
}

func (m *Manager) unregister(s *slot) {
	if s.reg == nil {
		return
	}
	if err := s.reg.Unregister(); err != nil {
		m.log.Warn("could not unregister a shortcut", "purpose", string(s.purpose), "shortcut", s.shortcut, "err", err)
	}
	s.reg, s.events = nil, nil
}

func (m *Manager) fail(p Purpose, shortcut string, err error) {
	rerr := &RegisterError{Purpose: p, Shortcut: shortcut, Err: err}
	m.log.Warn("shortcut not registered", "purpose", string(p), "shortcut", shortcut, "err", err)
	if m.opts.OnError != nil {
		m.opts.OnError(rerr)
	}
}

func (m *Manager) close() {
	m.unregister(&m.transcribe)
	m.unregister(&m.postProcess)
	m.unregister(&m.cancel)
	m.closed = true
}
