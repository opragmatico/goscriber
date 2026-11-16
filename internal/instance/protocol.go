package instance

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// Command asks the running app to do something.
type Command string

const (
	// Toggle starts dictation, or stops it and transcribes what was said, as
	// the shortcut does in toggle mode.
	Toggle Command = "toggle-transcription"
	// Cancel discards the dictation in progress.
	Cancel Command = "cancel"
	// Show brings the settings window to the front, as a second launch
	// without flags does.
	Show Command = "show-settings"
)

// Valid reports whether c is a command the app knows.
func (c Command) Valid() bool {
	switch c {
	case Toggle, Cancel, Show:
		return true
	}
	return false
}

// Handler runs a command in the app. It must not block: the app hands the
// command on (to the dictation coordinator, to the main thread) and returns.
// An error is sent back to the command line.
type Handler func(Command) error

// ProtocolVersion is the version of the control protocol. A request of
// another version is refused, so a newer command line talking to an older
// app says so instead of guessing.
const ProtocolVersion = 1

var (
	// ErrRunning is returned by Acquire when another Goscriber holds the
	// lock.
	ErrRunning = errors.New("another Goscriber is running")
	// ErrNotRunning is returned by Send when no Goscriber holds the lock.
	ErrNotRunning = errors.New("no Goscriber is running")
	// ErrRefused wraps the error the running app answered a command with.
	ErrRefused = errors.New("the running Goscriber refused the command")
	// ErrUnsupported is returned where there is no single-instance lock and
	// control socket yet (Windows, which will use a named pipe).
	ErrUnsupported = fmt.Errorf("single instance and remote control are not supported on %s yet: %w",
		runtime.GOOS, errors.ErrUnsupported)
)

const (
	lockName   = "goscriber.lock"
	socketName = "goscriber.sock"
	// maxSocketPath leaves room in sun_path, 104 bytes on macOS and 108 on
	// Linux, for the terminating NUL.
	maxSocketPath = 100
	// maxMessage bounds a request or response line.
	maxMessage = 4 << 10
	// ioTimeout bounds one exchange on the socket, on both ends.
	ioTimeout = 5 * time.Second
)

// LockPath is the lock file of the Goscriber whose config directory is dir.
func LockPath(dir string) string { return filepath.Join(dir, lockName) }

// SocketPath is the control socket of the Goscriber whose config directory
// is dir: goscriber.sock in it, or, when that path is too long for a socket
// address, a socket named after dir in a private directory under
// XDG_RUNTIME_DIR or the temporary directory.
func SocketPath(dir string) string {
	p := filepath.Join(dir, socketName)
	if len(p) <= maxSocketPath {
		return p
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	sum := sha256.Sum256([]byte(dir))
	return filepath.Join(base, "goscriber-"+strconv.Itoa(os.Getuid()), hex.EncodeToString(sum[:8])+".sock")
}

type request struct {
	Version int     `json:"version"`
	Command Command `json:"command"`
}

type response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// writeMessage writes v as one line of JSON.
func writeMessage(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// readMessage reads one line of JSON, at most maxMessage bytes, into v.
func readMessage(r io.Reader, v any) error {
	br := bufio.NewReaderSize(io.LimitReader(r, maxMessage), maxMessage)
	line, err := br.ReadSlice('\n')
	switch {
	case errors.Is(err, bufio.ErrBufferFull) || (errors.Is(err, io.EOF) && len(line) >= maxMessage):
		return fmt.Errorf("message longer than %d bytes", maxMessage)
	case errors.Is(err, io.EOF) && len(line) > 0:
		return io.ErrUnexpectedEOF
	case err != nil:
		return err
	}
	return json.Unmarshal(line, v)
}

// serveConn answers one request on rw with h.
func serveConn(rw io.ReadWriter, h Handler) error {
	var req request
	if err := readMessage(rw, &req); err != nil {
		if errors.Is(err, io.EOF) {
			return err // the client hung up without asking anything
		}
		return writeMessage(rw, response{Error: "malformed request: " + err.Error()})
	}
	var resp response
	switch {
	case req.Version != ProtocolVersion:
		resp.Error = fmt.Sprintf("protocol version %d is not supported; this Goscriber speaks version %d", req.Version, ProtocolVersion)
	case !req.Command.Valid():
		resp.Error = fmt.Sprintf("unknown command %q", req.Command)
	default:
		if err := h(req.Command); err != nil {
			resp.Error = err.Error()
		} else {
			resp.OK = true
		}
	}
	return writeMessage(rw, resp)
}

// exchange sends cmd on rw and returns the app's answer.
func exchange(rw io.ReadWriter, cmd Command) error {
	if err := writeMessage(rw, request{Version: ProtocolVersion, Command: cmd}); err != nil {
		return fmt.Errorf("instance: send %s: %w", cmd, err)
	}
	var resp response
	if err := readMessage(rw, &resp); err != nil {
		return fmt.Errorf("instance: no answer to %s: %w", cmd, err)
	}
	if !resp.OK {
		return fmt.Errorf("%w: %s", ErrRefused, resp.Error)
	}
	return nil
}
