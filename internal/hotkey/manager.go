package hotkey

import (
	"errors"
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
	// Register makes b a global shortcut. The OS stops delivering its key
	// presses to the focused app while it is registered.
	Register(b Binding) (Registration, error)
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
	Transcribe    string // the transcribe shortcut, as Parse reads it
	Cancel        string // the cancel shortcut, registered only while recording
	Mode          config.ActivationMode
	HoldThreshold time.Duration
}

// ConfigFrom returns the Config the settings describe.
func ConfigFrom(s config.Settings) Config {
	return Config{
		Transcribe:    s.TranscribeShortcut,
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
	return fmt.Sprintf("the %s shortcut %q is not available: %v", e.Purpose, e.Shortcut, e.Err)
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

// Manager registers the transcribe and cancel shortcuts and turns their key
// events into actions through an Activator.
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
	cfg        Config
	applied    bool
	act        Activator
	recording  bool // dictation confirmed a recording in progress
	transcribe slot
	cancel     slot
	closed     bool
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
		be:         be,
		opts:       opts,
		log:        log,
		now:        now,
		wake:       make(chan struct{}, 1),
		done:       make(chan struct{}),
		transcribe: slot{purpose: PurposeTranscribe},
		cancel:     slot{purpose: PurposeCancel},
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
			if ev == KeyDown {
				m.dispatch(m.act.Press(m.now()))
			} else {
				m.dispatch(m.act.Release(m.now()))
			}
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
	cancelChanged := first || cfg.Cancel != m.cfg.Cancel || cfg.Transcribe != m.cfg.Transcribe
	m.cfg = cfg

	if transcribeChanged {
		m.unregister(&m.transcribe)
		m.transcribe.shortcut = cfg.Transcribe
		// A key held during the switch belongs to the old shortcut.
		m.act.down, m.act.ownsPress = false, false
		if b, err := m.binding(PurposeTranscribe, cfg.Transcribe); err == nil {
			m.register(&m.transcribe, b)
		}
	}
	if cancelChanged {
		m.unregister(&m.cancel)
		m.cancel.shortcut = cfg.Cancel
		m.syncCancel()
	}
}

// binding parses and validates a configured shortcut, reporting a problem
// to OnError.
func (m *Manager) binding(p Purpose, shortcut string) (Binding, error) {
	b, err := ParseFor(shortcut, p)
	if err == nil && p == PurposeCancel {
		if t, terr := Parse(m.cfg.Transcribe); terr == nil && t == b {
			err = errors.New("it is the transcribe shortcut too")
		}
	}
	if err != nil {
		m.fail(p, shortcut, err)
		return Binding{}, err
	}
	return b, nil
}

func (m *Manager) setRecording(on bool) {
	m.act.SetRecording(on)
	m.recording = on
	m.syncCancel()
}

// syncCancel registers the cancel shortcut while dictation is recording and
// unregisters it otherwise.
func (m *Manager) syncCancel() {
	want := m.recording && m.applied
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
	reg, err := m.be.Register(b)
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
	m.unregister(&m.cancel)
	m.closed = true
}
