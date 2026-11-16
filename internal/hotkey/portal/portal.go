// Package portal binds Goscriber's shortcuts through the XDG desktop
// portal's GlobalShortcuts interface, the way a Wayland app owns a global
// shortcut: the desktop asks the user to confirm the keys, keeps them, and
// says when they go down and up.
//
// It is plain Go over D-Bus and builds on every OS; only Linux uses it.
package portal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/opragmatico/goscriber/internal/hotkey"
)

// AppID is Goscriber's application ID: the name of the desktop file the
// Linux packages install, by which the portal knows the app.
const AppID = "io.github.opragmatico.goscriber"

// D-Bus names of the portal.
const (
	busName        = "org.freedesktop.portal.Desktop"
	objectPath     = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	shortcutsIface = "org.freedesktop.portal.GlobalShortcuts"
	requestIface   = "org.freedesktop.portal.Request"
	sessionIface   = "org.freedesktop.portal.Session"
	registryIface  = "org.freedesktop.host.portal.Registry"
	propsIface     = "org.freedesktop.DBus.Properties"
)

// Errors.
var (
	// ErrUnavailable is what Open returns when the session has no portal
	// with the GlobalShortcuts interface.
	ErrUnavailable = errors.New("the desktop has no global shortcuts portal")
	// ErrRefused is the binding's error when the user dismissed the
	// desktop's shortcuts dialog.
	ErrRefused = errors.New("the desktop's shortcuts dialog was dismissed")
	// ErrNotBound is what Register returns for a shortcut the desktop
	// left out when it bound the others.
	ErrNotBound = errors.New("the desktop did not bind it")
	// ErrClosed is what Register returns after Close.
	ErrClosed = errors.New("the shortcuts portal is closed")
)

// State is where binding the shortcuts stands.
type State int

// States.
const (
	// Asking: the desktop has been asked to bind the shortcuts, and may be
	// showing the user its dialog.
	Asking State = iota
	// Bound: the desktop bound the shortcuts and reports their keys.
	Bound
	// Refused: the user dismissed the desktop's dialog.
	Refused
	// Failed: the portal could not bind the shortcuts, or stopped.
	Failed
)

func (s State) String() string {
	switch s {
	case Asking:
		return "asking"
	case Bound:
		return "bound"
	case Refused:
		return "refused"
	default:
		return "failed"
	}
}

// Status is what the portal bound.
type Status struct {
	State State
	// Triggers are the keys the desktop bound, by purpose, in its own
	// words, such as "Ctrl+Alt+Space". A shortcut bound without keys has
	// "", and one the desktop left out is missing.
	Triggers map[hotkey.Purpose]string
	// Err is why the shortcuts are not bound, when State is Refused or
	// Failed.
	Err error
	// Configurable is whether Configure can open the desktop's dialog to
	// change the keys (version 2 of the interface).
	Configurable bool
}

func (s Status) clone() Status {
	if s.Triggers != nil {
		t := make(map[hotkey.Purpose]string, len(s.Triggers))
		for p, d := range s.Triggers {
			t[p] = d
		}
		s.Triggers = t
	}
	return s
}

// Options configure a Backend.
type Options struct {
	// Suggest returns the shortcuts to suggest to the desktop, as settings
	// store them. The desktop asks the user about them the first time
	// Goscriber binds its shortcuts and keeps the user's answer, so later
	// suggestions change nothing. Nil suggests no keys.
	Suggest func() (transcribe, cancel string)
	// OnStatus is told each change of Status, in order, from the
	// Backend's goroutines. It must not block for long.
	OnStatus func(Status)
	// Conn is the bus connection to use, which the Backend leaves open.
	// Nil connects to the session bus, and Close closes that connection.
	Conn *dbus.Conn
	// Timeout bounds each portal call that waits for no user. Zero means
	// five seconds.
	Timeout time.Duration
	// Log receives debug lines. Nil means slog's default logger.
	Log *slog.Logger
}

// Backend is a hotkey.Backend whose shortcuts the desktop binds through
// the GlobalShortcuts portal. Open asks the desktop to bind the transcribe
// and cancel shortcuts in one portal session, which lasts until Close:
// the portal lets a session bind its shortcuts only once. Register only
// connects a purpose's key events to a Registration, so the Manager may
// register and unregister as often as it likes without asking the desktop
// again.
type Backend struct {
	conn    *dbus.Conn
	ownConn bool
	opts    Options
	log     *slog.Logger
	timeout time.Duration
	version uint32
	sender  string // the connection's unique name, as request paths spell it

	ctx     context.Context
	cancel  context.CancelFunc
	signals chan *dbus.Signal
	wg      sync.WaitGroup
	once    sync.Once

	reportMu sync.Mutex // keeps OnStatus calls in order

	mu      sync.Mutex
	closed  bool
	session dbus.ObjectPath
	status  Status
	waiters map[dbus.ObjectPath]chan response
	regs    map[hotkey.Purpose]*registration
	down    map[string]bool
}

// Open connects to the portal and asks the desktop to bind the shortcuts
// in the background; OnStatus follows the answer. It returns
// ErrUnavailable, wrapped, when the session has no GlobalShortcuts portal.
// ctx bounds connecting and checking the portal, not the binding.
func Open(ctx context.Context, opts Options) (*Backend, error) {
	conn, own := opts.Conn, false
	if conn == nil {
		// Not dbus.WithContext(ctx): the connection would close with ctx.
		c, err := dbus.ConnectSessionBus()
		if err != nil {
			return nil, fmt.Errorf("%w: no session bus: %v", ErrUnavailable, err)
		}
		conn, own = c, true
	}
	b := &Backend{
		conn:    conn,
		ownConn: own,
		opts:    opts,
		log:     opts.Log,
		timeout: opts.Timeout,
		signals: make(chan *dbus.Signal, 32),
		waiters: map[dbus.ObjectPath]chan response{},
		regs:    map[hotkey.Purpose]*registration{},
		down:    map[string]bool{},
	}
	if b.log == nil {
		b.log = slog.Default()
	}
	if b.timeout <= 0 {
		b.timeout = 5 * time.Second
	}
	if err := b.open(ctx); err != nil {
		if own {
			_ = conn.Close()
		}
		return nil, err
	}
	b.status = Status{State: Asking, Configurable: b.version >= 2}
	b.ctx, b.cancel = context.WithCancel(context.Background())
	b.conn.Signal(b.signals)
	b.wg.Go(b.dispatch)
	b.wg.Go(b.bind)
	return b, nil
}

func (b *Backend) open(ctx context.Context) error {
	names := b.conn.Names()
	if len(names) == 0 {
		return fmt.Errorf("%w: the bus gave no name", ErrUnavailable)
	}
	b.sender = strings.ReplaceAll(strings.TrimPrefix(names[0], ":"), ".", "_")
	obj := b.conn.Object(busName, objectPath)

	// A program the desktop did not start from its desktop file is known
	// to the portal only if it says who it is, before any portal call.
	// Older portals lack the registry, and one that already knows the app
	// refuses: either way the portal goes on as before.
	if err := obj.CallWithContext(ctx, registryIface+".Register", 0, AppID, map[string]dbus.Variant{}).Err; err != nil {
		b.log.Debug("the portal did not register the app ID", "app_id", AppID, "err", err)
	}

	var v dbus.Variant
	if err := obj.CallWithContext(ctx, propsIface+".Get", 0, shortcutsIface, "version").Store(&v); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	version, ok := v.Value().(uint32)
	if !ok || version == 0 {
		return fmt.Errorf("%w: GlobalShortcuts version %v", ErrUnavailable, v.Value())
	}
	b.version = version

	requests := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + b.sender)
	sessions := dbus.ObjectPath("/org/freedesktop/portal/desktop/session/" + b.sender)
	for _, m := range [][]dbus.MatchOption{
		{dbus.WithMatchSender(busName), dbus.WithMatchObjectPath(objectPath), dbus.WithMatchInterface(shortcutsIface)},
		{dbus.WithMatchSender(busName), dbus.WithMatchInterface(requestIface), dbus.WithMatchMember("Response"), dbus.WithMatchPathNamespace(requests)},
		{dbus.WithMatchSender(busName), dbus.WithMatchInterface(sessionIface), dbus.WithMatchMember("Closed"), dbus.WithMatchPathNamespace(sessions)},
	} {
		if err := b.conn.AddMatchSignalContext(ctx, m...); err != nil {
			return fmt.Errorf("%w: cannot follow the portal's signals: %v", ErrUnavailable, err)
		}
	}
	return nil
}

// Version is the version of the portal's GlobalShortcuts interface.
func (b *Backend) Version() uint32 { return b.version }

// Status is what the portal has bound so far.
func (b *Backend) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status.clone()
}

// Register connects the key events of the shortcut for purpose p to a
// Registration. The desktop owns the keys, so the binding is ignored: it
// was suggested to the desktop when Open asked for the shortcuts. Register
// fails once the binding failed or the desktop left p out; while the
// desktop is still asking, the Registration hears nothing until it binds.
func (b *Backend) Register(p hotkey.Purpose, _ hotkey.Binding) (hotkey.Registration, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.closed:
		return nil, ErrClosed
	case b.status.State == Refused, b.status.State == Failed:
		return nil, b.status.Err
	case b.regs[p] != nil:
		return nil, fmt.Errorf("the %s shortcut is already registered", p)
	}
	if b.status.State == Bound {
		if _, ok := b.status.Triggers[p]; !ok {
			return nil, ErrNotBound
		}
	}
	r := &registration{b: b, p: p, events: make(chan hotkey.KeyEvent, 16)}
	b.regs[p] = r
	return r, nil
}

// Configure opens the desktop's dialog to change the keys. It needs
// version 2 of the interface and a bound session.
func (b *Backend) Configure() error {
	if b.version < 2 {
		return errors.New("the desktop's shortcuts portal cannot change the keys; change them in the desktop's settings")
	}
	b.mu.Lock()
	session := b.session
	b.mu.Unlock()
	if session == "" {
		return errors.New("the desktop has not bound the shortcuts yet")
	}
	ctx, cancel := context.WithTimeout(b.ctx, b.timeout)
	defer cancel()
	return b.conn.Object(busName, objectPath).CallWithContext(ctx, shortcutsIface+".ConfigureShortcuts", 0,
		session, "", map[string]dbus.Variant{}).Err
}

// Close ends the portal session, which gives the keys back to the desktop,
// stops the Registrations' events and, if Open connected to the session
// bus, disconnects.
func (b *Backend) Close() error {
	b.once.Do(func() {
		b.cancel()
		b.mu.Lock()
		b.closed = true
		session := b.session
		for p, r := range b.regs {
			close(r.events)
			delete(b.regs, p)
		}
		b.mu.Unlock()
		if session != "" {
			ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
			if err := b.conn.Object(busName, session).CallWithContext(ctx, sessionIface+".Close", 0).Err; err != nil {
				b.log.Debug("could not close the shortcuts portal session", "err", err)
			}
			cancel()
		}
		b.conn.RemoveSignal(b.signals)
		if b.ownConn {
			_ = b.conn.Close()
		}
		b.wg.Wait()
	})
	return nil
}

// shortcut is a shortcut as the portal lists them: a(sa{sv}).
type shortcut struct {
	ID    string
	Props map[string]dbus.Variant
}

// Descriptions of the shortcuts, which the desktop shows.
const (
	TranscribeDescription = "Start and stop dictation"
	CancelDescription     = "Cancel dictation"
)

func (b *Backend) shortcuts() []shortcut {
	var transcribe, cancel string
	if b.opts.Suggest != nil {
		transcribe, cancel = b.opts.Suggest()
	}
	entry := func(p hotkey.Purpose, description, suggested string) shortcut {
		props := map[string]dbus.Variant{"description": dbus.MakeVariant(description)}
		if t := Trigger(p, suggested); t != "" {
			props["preferred_trigger"] = dbus.MakeVariant(t)
		}
		return shortcut{ID: string(p), Props: props}
	}
	return []shortcut{
		entry(hotkey.PurposeTranscribe, TranscribeDescription, transcribe),
		entry(hotkey.PurposeCancel, CancelDescription, cancel),
	}
}

// bind creates the portal session and asks the desktop to bind the
// shortcuts, which may wait for the user as long as they like.
func (b *Backend) bind() {
	b.update(func(*Status) {}) // Asking, before any answer
	ctx, cancel := context.WithTimeout(b.ctx, b.timeout)
	results, err := b.request(ctx, "CreateSession", map[string]dbus.Variant{"session_handle_token": dbus.MakeVariant(token())})
	cancel()
	if err != nil {
		b.fail(fmt.Errorf("cannot start a shortcuts session: %w", err))
		return
	}
	var session dbus.ObjectPath
	switch h := results["session_handle"].Value().(type) {
	case string: // the interface's type, by a historical mistake
		session = dbus.ObjectPath(h)
	case dbus.ObjectPath:
		session = h
	}
	if !session.IsValid() {
		b.fail(fmt.Errorf("the portal returned no session: %v", results["session_handle"]))
		return
	}
	b.mu.Lock()
	b.session = session
	b.mu.Unlock()

	results, err = b.request(b.ctx, "BindShortcuts", session, b.shortcuts(), "", map[string]dbus.Variant{})
	if err != nil {
		b.fail(err)
		return
	}
	var bound []shortcut
	if v, ok := results["shortcuts"]; ok {
		if err := v.Store(&bound); err != nil {
			b.fail(fmt.Errorf("cannot read the bound shortcuts: %w", err))
			return
		}
	}
	triggers := triggersOf(bound)
	b.log.Info("the desktop bound the shortcuts", "shortcuts", triggers)
	b.update(func(s *Status) { s.State, s.Triggers, s.Err = Bound, triggers, nil })
}

func triggersOf(list []shortcut) map[hotkey.Purpose]string {
	m := make(map[hotkey.Purpose]string, len(list))
	for _, s := range list {
		d, _ := s.Props["trigger_description"].Value().(string)
		m[hotkey.Purpose(s.ID)] = d
	}
	return m
}

// fail records why the shortcuts are not bound. A failure from Close
// cancelling the binding is not one.
func (b *Backend) fail(err error) {
	if b.ctx.Err() != nil {
		return
	}
	state := Failed
	if errors.Is(err, ErrRefused) {
		state = Refused
	}
	b.log.Warn("the desktop did not bind the shortcuts", "err", err)
	b.update(func(s *Status) { s.State, s.Err = state, err })
}

// update changes the status and reports it, in order.
func (b *Backend) update(f func(*Status)) {
	b.reportMu.Lock()
	defer b.reportMu.Unlock()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	f(&b.status)
	st := b.status.clone()
	b.mu.Unlock()
	if b.opts.OnStatus != nil {
		b.opts.OnStatus(st)
	}
}

// response is a Request's Response signal.
type response struct {
	code    uint32
	results map[string]dbus.Variant
}

// request calls a portal method that answers through a Request object and
// waits for the answer. The options vardict is the last argument; request
// adds the handle token to it.
func (b *Backend) request(ctx context.Context, method string, args ...any) (map[string]dbus.Variant, error) {
	tok := token()
	args[len(args)-1].(map[string]dbus.Variant)["handle_token"] = dbus.MakeVariant(tok)
	// Wait for the answer before asking, so it cannot come first.
	want := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + b.sender + "/" + tok)
	ch := make(chan response, 1)
	b.waitFor(want, ch)
	defer b.stopWaiting(want)

	var handle dbus.ObjectPath
	if err := b.conn.Object(busName, objectPath).CallWithContext(ctx, shortcutsIface+"."+method, 0, args...).Store(&handle); err != nil {
		return nil, err
	}
	if handle != want {
		// A portal older than 0.9 answers on a path of its own, outside
		// the requests followed since Open.
		b.waitFor(handle, ch)
		defer b.stopWaiting(handle)
		match := []dbus.MatchOption{
			dbus.WithMatchSender(busName), dbus.WithMatchObjectPath(handle),
			dbus.WithMatchInterface(requestIface), dbus.WithMatchMember("Response"),
		}
		if err := b.conn.AddMatchSignalContext(ctx, match...); err != nil {
			return nil, fmt.Errorf("cannot follow the answer to %s: %w", method, err)
		}
		defer func() { _ = b.conn.RemoveMatchSignalContext(context.Background(), match...) }()
	}
	select {
	case r := <-ch:
		switch r.code {
		case 0:
			return r.results, nil
		case 1:
			return nil, ErrRefused
		default:
			return nil, fmt.Errorf("the portal ended the %s request", method)
		}
	case <-ctx.Done():
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = b.conn.Object(busName, handle).CallWithContext(closeCtx, requestIface+".Close", 0).Err
		return nil, ctx.Err()
	}
}

func (b *Backend) waitFor(path dbus.ObjectPath, ch chan response) {
	b.mu.Lock()
	b.waiters[path] = ch
	b.mu.Unlock()
}

func (b *Backend) stopWaiting(path dbus.ObjectPath) {
	b.mu.Lock()
	delete(b.waiters, path)
	b.mu.Unlock()
}

// dispatch handles the portal's signals until Close.
func (b *Backend) dispatch() {
	for {
		select {
		case <-b.ctx.Done():
			return
		case sig, ok := <-b.signals:
			if !ok {
				b.fail(errors.New("the connection to the session bus closed"))
				return
			}
			b.handle(sig)
		}
	}
}

func (b *Backend) handle(sig *dbus.Signal) {
	switch sig.Name {
	case requestIface + ".Response":
		var r response
		if err := dbus.Store(sig.Body, &r.code, &r.results); err != nil {
			b.log.Debug("unreadable portal response", "path", sig.Path, "err", err)
			return
		}
		b.mu.Lock()
		ch := b.waiters[sig.Path]
		delete(b.waiters, sig.Path)
		b.mu.Unlock()
		if ch != nil {
			ch <- r
		}
	case shortcutsIface + ".Activated", shortcutsIface + ".Deactivated":
		var (
			session dbus.ObjectPath
			id      string
			stamp   uint64
			opts    map[string]dbus.Variant
		)
		if err := dbus.Store(sig.Body, &session, &id, &stamp, &opts); err != nil {
			b.log.Debug("unreadable shortcut signal", "signal", sig.Name, "err", err)
			return
		}
		b.key(session, id, sig.Name == shortcutsIface+".Activated")
	case shortcutsIface + ".ShortcutsChanged":
		var (
			session dbus.ObjectPath
			list    []shortcut
		)
		if err := dbus.Store(sig.Body, &session, &list); err != nil {
			b.log.Debug("unreadable ShortcutsChanged signal", "err", err)
			return
		}
		b.mu.Lock()
		ours := session == b.session && b.status.State == Bound
		b.mu.Unlock()
		if ours {
			triggers := triggersOf(list)
			b.log.Info("the desktop changed the shortcuts", "shortcuts", triggers)
			b.update(func(s *Status) { s.Triggers = triggers })
		}
	case sessionIface + ".Closed":
		b.mu.Lock()
		ours := sig.Path == b.session
		b.mu.Unlock()
		if ours {
			b.fail(errors.New("the desktop ended the shortcuts session"))
		}
	}
}

// key delivers a shortcut going down or up to its Registration: one
// key-down per press, whatever the desktop repeats.
func (b *Backend) key(session dbus.ObjectPath, id string, down bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if session != b.session || b.closed || b.down[id] == down {
		return
	}
	b.down[id] = down
	r := b.regs[hotkey.Purpose(id)]
	if r == nil {
		return
	}
	ev := hotkey.KeyUp
	if down {
		ev = hotkey.KeyDown
	}
	select {
	case r.events <- ev:
	default:
		b.log.Warn("dropped a shortcut event: nobody is reading them", "shortcut", id)
	}
}

type registration struct {
	b      *Backend
	p      hotkey.Purpose
	events chan hotkey.KeyEvent
}

func (r *registration) Events() <-chan hotkey.KeyEvent { return r.events }

// Unregister stops the events. The desktop keeps the keys until Close.
func (r *registration) Unregister() error {
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	if r.b.regs[r.p] == r {
		delete(r.b.regs, r.p)
		close(r.events)
	}
	return nil
}

// token is a fresh handle token: unique and hard to guess, as the portal
// asks.
func token() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "goscriber_" + hex.EncodeToString(buf[:])
}
