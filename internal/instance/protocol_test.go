package instance

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
)

// recorder is a Handler that remembers the commands it ran.
type recorder struct {
	mu   sync.Mutex
	cmds []Command
	err  error
}

func (r *recorder) handle(c Command) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmds = append(r.cmds, c)
	return r.err
}

func (r *recorder) got() []Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Command(nil), r.cmds...)
}

// pipeExchange runs exchange against serveConn over an in-memory pipe.
func pipeExchange(t *testing.T, cmd Command, h Handler) error {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		done <- serveConn(server, h)
	}()
	err := exchange(client, cmd)
	if serr := <-done; serr != nil {
		t.Errorf("serveConn: %v", serr)
	}
	return err
}

func TestExchangeRunsEachCommand(t *testing.T) {
	var r recorder
	for _, c := range []Command{Toggle, Cancel, Show} {
		if err := pipeExchange(t, c, r.handle); err != nil {
			t.Fatalf("%s: %v", c, err)
		}
	}
	if got := r.got(); len(got) != 3 || got[0] != Toggle || got[1] != Cancel || got[2] != Show {
		t.Errorf("handler ran %v", got)
	}
}

// The handler's error comes back to the command line, marked as a refusal.
func TestExchangeReturnsTheHandlersError(t *testing.T) {
	r := recorder{err: errors.New("Goscriber is quitting")}
	err := pipeExchange(t, Toggle, r.handle)
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "Goscriber is quitting") {
		t.Errorf("err = %v, want a refusal with the handler's reason", err)
	}
}

// rawRequest writes line to serveConn and returns the response it wrote.
func rawRequest(t *testing.T, line string, h Handler) response {
	t.Helper()
	var out bytes.Buffer
	rw := struct {
		io.Reader
		io.Writer
	}{strings.NewReader(line), &out}
	if err := serveConn(rw, h); err != nil {
		t.Fatalf("serveConn(%q): %v", line, err)
	}
	if !strings.HasSuffix(out.String(), "\n") || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("response %q is not one line", out.String())
	}
	var resp response
	if err := readMessage(&out, &resp); err != nil {
		t.Fatalf("response %q: %v", out.String(), err)
	}
	return resp
}

func TestServeConnRefusesBadRequests(t *testing.T) {
	for _, tt := range []struct {
		name, line, want string
	}{
		{"another version", `{"version":2,"command":"cancel"}` + "\n", "protocol version 2"},
		{"no version", `{"command":"cancel"}` + "\n", "protocol version 0"},
		{"unknown command", `{"version":1,"command":"explode"}` + "\n", `unknown command "explode"`},
		{"not JSON", "hello\n", "malformed request"},
		{"no newline", `{"version":1,"command":"cancel"}`, "malformed request"},
		{"too long", strings.Repeat("x", maxMessage+10) + "\n", "longer than"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var r recorder
			resp := rawRequest(t, tt.line, r.handle)
			if resp.OK || !strings.Contains(resp.Error, tt.want) {
				t.Errorf("response %+v, want a refusal containing %q", resp, tt.want)
			}
			if got := r.got(); len(got) != 0 {
				t.Errorf("handler ran %v for a bad request", got)
			}
		})
	}
}

// The wire format is part of the contract with older and newer command
// lines: one JSON line each way.
func TestWireFormat(t *testing.T) {
	var out bytes.Buffer
	if err := writeMessage(&out, request{Version: ProtocolVersion, Command: Toggle}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), `{"version":1,"command":"toggle-transcription"}`+"\n"; got != want {
		t.Errorf("request %q, want %q", got, want)
	}
	var r recorder
	if resp := rawRequest(t, `{"version":1,"command":"show-settings"}`+"\n", r.handle); !resp.OK || resp.Error != "" {
		t.Errorf("response %+v, want ok", resp)
	}
	out.Reset()
	if err := writeMessage(&out, response{OK: true}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != `{"ok":true}`+"\n" {
		t.Errorf("response %q", got)
	}
}

// A client that connects and hangs up gets no answer and causes no error
// worth logging.
func TestServeConnClientHangsUp(t *testing.T) {
	var r recorder
	rw := struct {
		io.Reader
		io.Writer
	}{strings.NewReader(""), io.Discard}
	if err := serveConn(rw, r.handle); !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want EOF", err)
	}
}

func TestSocketPathFallsBackWhenTooLong(t *testing.T) {
	short := "/home/someone/.config/goscriber"
	if got := SocketPath(short); got != short+"/goscriber.sock" {
		t.Errorf("SocketPath(%q) = %q", short, got)
	}
	long := "/" + strings.Repeat("deep/", 30) + "goscriber"
	got := SocketPath(long)
	if strings.HasPrefix(got, long) || len(got) > maxSocketPath {
		t.Errorf("SocketPath(long) = %q (%d bytes), want a short path elsewhere", got, len(got))
	}
	if again := SocketPath(long); again != got {
		t.Errorf("SocketPath is not stable: %q then %q", got, again)
	}
	if other := SocketPath(long + "2"); other == got {
		t.Errorf("two directories share the socket %q", got)
	}
}

func TestCommandValid(t *testing.T) {
	for _, c := range []Command{Toggle, Cancel, Show} {
		if !c.Valid() {
			t.Errorf("%q is not valid", c)
		}
	}
	if Command("").Valid() || Command("quit").Valid() {
		t.Error("an unknown command is valid")
	}
}
