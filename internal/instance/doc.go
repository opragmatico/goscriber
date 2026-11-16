// Package instance keeps a single Goscriber running and serves the control
// socket that remote-control CLI flags talk to.
//
// The running app holds an exclusive flock on goscriber.lock in the config
// directory for as long as it runs, so a second launch knows at once that it
// is not alone, even when both start together; the kernel releases the lock
// when the process ends, however it ends. Next to the lock it listens on a
// Unix domain socket, goscriber.sock, readable only by the user. A path too
// long for a socket address (sun_path holds about 100 bytes) moves the socket
// to a private directory under XDG_RUNTIME_DIR or the temporary directory.
//
// The protocol is one request and one response per connection, each a line
// of JSON: {"version":1,"command":"toggle-transcription"} answered by
// {"ok":true} or {"ok":false,"error":"…"}. Windows, which would use a named
// pipe, is not supported yet: Acquire and Send return ErrUnsupported.
package instance
