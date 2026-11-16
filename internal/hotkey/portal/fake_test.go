package portal

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// newBus starts a private D-Bus daemon for the test and returns its
// address. It skips the test where dbus-daemon is not installed.
func newBus(t *testing.T) string {
	t.Helper()
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is not installed")
	}
	// Unix socket paths are short: keep the directory near the root.
	dir, err := os.MkdirTemp("", "gsbus")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	conf := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(conf, []byte(`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:dir=`+dir+`</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(daemon, "--config-file="+conf, "--nofork", "--nopidfile", "--print-address")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("dbus-daemon printed no address: %v", err)
	}
	return strings.TrimSpace(line)
}

// connect opens a connection to the bus at addr, closed with the test.
func connect(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// fakePortal is the desktop's portal on a test bus: the GlobalShortcuts
// interface, the host app registry and the version property.
type fakePortal struct {
	t       *testing.T
	conn    *dbus.Conn
	version uint32

	mu         sync.Mutex
	appID      string
	asked      []shortcut // what BindShortcuts asked for
	session    dbus.ObjectPath
	closed     []dbus.ObjectPath // sessions closed
	configured []dbus.ObjectPath // sessions ConfigureShortcuts was called for
	// answer is BindShortcuts' answer: the response code and the shortcuts
	// bound. Nil binds every shortcut asked for, described by its
	// preferred trigger.
	answer func(asked []shortcut) (uint32, []shortcut)
	// hold, when set, delays BindShortcuts' answer until it closes.
	hold chan struct{}
	// ownHandles answers on request paths of the portal's own, as portals
	// older than 0.9 did, a moment after the call returns.
	ownHandles bool
}

// newFakePortal exports a fake portal with GlobalShortcuts version on the
// bus at addr, after setup, if any, has set its answers. Version 0 exports
// no GlobalShortcuts at all.
func newFakePortal(t *testing.T, addr string, version uint32, setup ...func(*fakePortal)) *fakePortal {
	t.Helper()
	f := &fakePortal{t: t, conn: connect(t, addr), version: version}
	for _, s := range setup {
		s(f)
	}
	if version > 0 {
		if err := f.conn.Export(shortcutsAPI{f}, objectPath, shortcutsIface); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.conn.Export(registryAPI{f}, objectPath, registryIface); err != nil {
		t.Fatal(err)
	}
	if err := f.conn.Export(propsAPI{f}, objectPath, propsIface); err != nil {
		t.Fatal(err)
	}
	reply, err := f.conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("cannot own %s: %v %v", busName, reply, err)
	}
	return f
}

// requestPath is where the portal answers a request from sender.
func (f *fakePortal) requestPath(sender dbus.Sender, opts map[string]dbus.Variant) dbus.ObjectPath {
	if f.ownHandles {
		return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/old/" + token())
	}
	tok, _ := opts["handle_token"].Value().(string)
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + escape(string(sender)) + "/" + tok)
}

func escape(sender string) string {
	return strings.ReplaceAll(strings.TrimPrefix(sender, ":"), ".", "_")
}

func (f *fakePortal) respond(path dbus.ObjectPath, code uint32, results map[string]dbus.Variant) {
	if f.ownHandles {
		// The caller follows such a path once the call returns; an answer
		// that came first would be lost, as with those old portals.
		time.Sleep(100 * time.Millisecond)
	}
	// An answer may come after the test, when the bus is gone.
	_ = f.conn.Emit(path, requestIface+".Response", code, results)
}

// emit sends a GlobalShortcuts signal.
func (f *fakePortal) emit(member string, args ...any) {
	f.t.Helper()
	if err := f.conn.Emit(objectPath, shortcutsIface+"."+member, args...); err != nil {
		f.t.Fatalf("emit %s: %v", member, err)
	}
}

// press sends Activated or Deactivated for the shortcut id in session.
func (f *fakePortal) press(session dbus.ObjectPath, id string, down bool) {
	f.t.Helper()
	member := "Deactivated"
	if down {
		member = "Activated"
	}
	f.emit(member, session, id, uint64(time.Now().UnixMilli()), map[string]dbus.Variant{})
}

func (f *fakePortal) currentSession() dbus.ObjectPath {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.session
}

func (f *fakePortal) askedFor() []shortcut {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked
}

func (f *fakePortal) closedSessions() []dbus.ObjectPath {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]dbus.ObjectPath(nil), f.closed...)
}

type shortcutsAPI struct{ f *fakePortal }

func (a shortcutsAPI) CreateSession(sender dbus.Sender, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	f := a.f
	tok, _ := opts["session_handle_token"].Value().(string)
	session := dbus.ObjectPath("/org/freedesktop/portal/desktop/session/" + escape(string(sender)) + "/" + tok)
	if err := f.conn.Export(sessionAPI{f, session}, session, sessionIface); err != nil {
		return "", dbus.MakeFailedError(err)
	}
	f.mu.Lock()
	f.session = session
	f.mu.Unlock()
	path := f.requestPath(sender, opts)
	go f.respond(path, 0, map[string]dbus.Variant{"session_handle": dbus.MakeVariant(string(session))})
	return path, nil
}

func (a shortcutsAPI) BindShortcuts(sender dbus.Sender, _ dbus.ObjectPath, shortcuts []shortcut, _ string, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	f := a.f
	f.mu.Lock()
	f.asked = shortcuts
	answer, hold := f.answer, f.hold
	f.mu.Unlock()
	path := f.requestPath(sender, opts)
	go func() {
		if hold != nil {
			<-hold
		}
		code, bound := uint32(0), []shortcut(nil)
		if answer != nil {
			code, bound = answer(shortcuts)
		} else {
			for _, s := range shortcuts {
				trigger, _ := s.Props["preferred_trigger"].Value().(string)
				bound = append(bound, described(s.ID, trigger))
			}
		}
		results := map[string]dbus.Variant{}
		if code == 0 {
			results["shortcuts"] = dbus.MakeVariant(bound)
		}
		f.respond(path, code, results)
	}()
	return path, nil
}

func (a shortcutsAPI) ConfigureShortcuts(session dbus.ObjectPath, _ string, _ map[string]dbus.Variant) *dbus.Error {
	f := a.f
	if f.version < 2 {
		return dbus.NewError("org.freedesktop.DBus.Error.UnknownMethod", []any{"no ConfigureShortcuts in version 1"})
	}
	f.mu.Lock()
	f.configured = append(f.configured, session)
	f.mu.Unlock()
	return nil
}

// described is a bound shortcut as the portal reports it.
func described(id, trigger string) shortcut {
	return shortcut{ID: id, Props: map[string]dbus.Variant{
		"description":         dbus.MakeVariant(id),
		"trigger_description": dbus.MakeVariant(trigger),
	}}
}

type sessionAPI struct {
	f    *fakePortal
	path dbus.ObjectPath
}

func (s sessionAPI) Close() *dbus.Error {
	s.f.mu.Lock()
	s.f.closed = append(s.f.closed, s.path)
	s.f.mu.Unlock()
	return nil
}

type registryAPI struct{ f *fakePortal }

func (r registryAPI) Register(appID string, _ map[string]dbus.Variant) *dbus.Error {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	if r.f.appID != "" {
		return dbus.NewError("org.freedesktop.portal.Error.Failed", []any{"already registered"})
	}
	r.f.appID = appID
	return nil
}

type propsAPI struct{ f *fakePortal }

func (p propsAPI) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	if iface != shortcutsIface || name != "version" || p.f.version == 0 {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownProperty", []any{iface + "." + name})
	}
	return dbus.MakeVariant(p.f.version), nil
}

// openTest opens a Backend on the bus at addr whose status changes go to
// the returned channel. It closes with the test.
func openTest(t *testing.T, addr string, opts Options) (*Backend, <-chan Status) {
	t.Helper()
	statuses := make(chan Status, 32)
	opts.Conn = connect(t, addr)
	opts.OnStatus = func(s Status) { statuses <- s }
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := Open(ctx, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b, statuses
}

// waitState waits for a status with state want, failing on any other
// final state.
func waitState(t *testing.T, statuses <-chan Status, want State) Status {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case s := <-statuses:
			if s.State == want {
				return s
			}
			if s.State != Asking {
				t.Fatalf("status %v (%v), want %v", s.State, s.Err, want)
			}
		case <-deadline:
			t.Fatalf("no %v status", want)
		}
	}
}
