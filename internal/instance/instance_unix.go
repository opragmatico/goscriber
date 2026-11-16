//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package instance

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// retryInterval is how often Send tries the socket again while the app that
// holds the lock is still starting.
const retryInterval = 50 * time.Millisecond

// Instance is the running app's hold on being the only Goscriber, with its
// control socket. Close releases both.
type Instance struct {
	lock      *os.File
	ln        *net.UnixListener
	listenErr error // why there is no socket, when ln is nil
	path      string

	mu        sync.Mutex
	closed    bool // no more connections are accepted
	served    bool
	wg        sync.WaitGroup // Serve and the connections it handles
	closeOnce sync.Once
	closeErr  error
}

// Acquire makes this process the Goscriber whose config directory is dir:
// it takes the lock, and returns ErrRunning when another process holds it.
// It then listens on the control socket, replacing one a crashed Goscriber
// left behind. A socket that cannot be made does not fail Acquire, which
// still holds the lock: Serve returns the reason.
func Acquire(dir string) (*Instance, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("instance: create %s: %w", dir, err)
	}
	f, err := os.OpenFile(LockPath(dir), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("instance: open the lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrRunning
		}
		return nil, fmt.Errorf("instance: lock %s: %w", f.Name(), err)
	}
	// The process ID, for whoever looks at the file; the lock is what counts.
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)

	in := &Instance{lock: f, path: SocketPath(dir)}
	in.ln, in.listenErr = listen(dir, in.path)
	return in, nil
}

// listen creates the control socket at path, which only the user can
// connect to.
func listen(dir, path string) (*net.UnixListener, error) {
	if parent := filepath.Dir(path); parent != filepath.Clean(dir) {
		if err := privateDir(parent); err != nil {
			return nil, err
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("instance: remove the old control socket: %w", err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("instance: control socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("instance: control socket: %w", err)
	}
	return ln, nil
}

// privateDir creates dir, for a socket outside the config directory, and
// checks that it is the user's own and closed to everyone else, as a shared
// temporary directory demands.
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("instance: create %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("instance: %w", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.IsDir():
		return fmt.Errorf("instance: %s is not a directory", dir)
	case !ok || int(st.Uid) != os.Getuid():
		return fmt.Errorf("instance: %s belongs to another user", dir)
	case fi.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("instance: %s is open to other users (%v)", dir, fi.Mode().Perm())
	}
	return nil
}

// SocketPath returns the control socket's path.
func (in *Instance) SocketPath() string { return in.path }

// Serve answers commands with h until ctx is done, then stops listening,
// removing the socket, and waits for the commands in progress. The lock is
// kept until Close, so a launch meanwhile still finds Goscriber running.
// Serve returns at once with the reason when there is no socket. Call it
// once.
func (in *Instance) Serve(ctx context.Context, h Handler) error {
	if in.ln == nil {
		return in.listenErr
	}
	in.mu.Lock()
	if in.closed || in.served {
		in.mu.Unlock()
		return nil
	}
	in.served = true
	in.wg.Add(1)
	in.mu.Unlock()
	defer in.wg.Done()

	stop := context.AfterFunc(ctx, in.stopListening)
	defer stop()
	for {
		conn, err := in.ln.AcceptUnix()
		if err != nil {
			if in.isClosed() {
				return nil
			}
			// Out of file descriptors, say: the next accept may work.
			time.Sleep(retryInterval)
			continue
		}
		in.mu.Lock()
		if in.closed {
			in.mu.Unlock()
			conn.Close()
			return nil
		}
		in.wg.Add(1)
		in.mu.Unlock()
		go func() {
			defer in.wg.Done()
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(ioTimeout))
			_ = serveConn(conn, h)
		}()
	}
}

// stopListening refuses new connections and removes the socket.
func (in *Instance) stopListening() {
	in.mu.Lock()
	in.closed = true
	in.mu.Unlock()
	if in.ln != nil {
		in.ln.Close() // unlinks the socket, which Acquire created
	}
}

func (in *Instance) isClosed() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.closed
}

// Close stops serving, waits for the commands in progress and releases the
// lock. It is safe on a nil Instance and to call more than once.
func (in *Instance) Close() error {
	if in == nil {
		return nil
	}
	in.closeOnce.Do(func() {
		in.stopListening()
		in.wg.Wait()
		_ = syscall.Flock(int(in.lock.Fd()), syscall.LOCK_UN)
		in.closeErr = in.lock.Close()
	})
	return in.closeErr
}

// Send hands cmd to the Goscriber whose config directory is dir and returns
// its answer: nil once the app has taken the command, ErrNotRunning when no
// Goscriber holds the lock, or an error wrapping ErrRefused with the app's
// reason. While the app that holds the lock is still starting, Send tries
// again until ctx is done, or for five seconds.
func Send(ctx context.Context, dir string, cmd Command) error {
	if !cmd.Valid() {
		return fmt.Errorf("instance: unknown command %q", cmd)
	}
	ctx, cancel := context.WithTimeout(ctx, ioTimeout)
	defer cancel()
	path := SocketPath(dir)
	var d net.Dialer
	for {
		conn, err := d.DialContext(ctx, "unix", path)
		if err == nil {
			defer conn.Close()
			if deadline, ok := ctx.Deadline(); ok {
				_ = conn.SetDeadline(deadline)
			}
			return exchange(conn, cmd)
		}
		if !held(dir) {
			return ErrNotRunning
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("instance: Goscriber is running but does not answer on %s: %w", path, err)
		case <-time.After(retryInterval):
		}
	}
}

// held reports whether a process holds the lock of dir's Goscriber. It
// takes a shared lock for a moment to find out.
func held(dir string) bool {
	f, err := os.Open(LockPath(dir))
	if err != nil {
		return false
	}
	defer f.Close()
	fd := int(f.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	_ = syscall.Flock(fd, syscall.LOCK_UN)
	return false
}
