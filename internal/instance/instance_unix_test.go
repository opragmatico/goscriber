//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package instance

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// socketDir creates the directory dir's socket goes in, for tests that
// listen on it themselves.
func socketDir(t *testing.T, dir string) string {
	t.Helper()
	path := SocketPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func acquire(t *testing.T, dir string) *Instance {
	t.Helper()
	in, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close() })
	return in
}

// serve runs in.Serve with h until the test ends.
func serve(t *testing.T, in *Instance, h Handler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- in.Serve(ctx, h) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
}

func send(t *testing.T, dir string, c Command) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return Send(ctx, dir, c)
}

func TestOnlyOneInstance(t *testing.T) {
	dir := t.TempDir()
	in := acquire(t, dir)
	if _, err := Acquire(dir); !errors.Is(err, ErrRunning) {
		t.Fatalf("second Acquire: %v, want ErrRunning", err)
	}
	if err := in.Close(); err != nil {
		t.Fatal(err)
	}
	if err := in.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	again := acquire(t, dir)
	if again.SocketPath() == "" {
		t.Error("no socket after the first instance closed")
	}
	// Another config directory is another Goscriber.
	acquire(t, t.TempDir())
}

func TestSendReachesTheRunningApp(t *testing.T) {
	dir := t.TempDir()
	in := acquire(t, dir)
	var r recorder
	serve(t, in, r.handle)

	for _, c := range []Command{Toggle, Cancel, Show} {
		if err := send(t, dir, c); err != nil {
			t.Fatalf("Send(%s): %v", c, err)
		}
	}
	if got := r.got(); len(got) != 3 || got[0] != Toggle || got[1] != Cancel || got[2] != Show {
		t.Errorf("app ran %v", got)
	}
	fi, err := os.Stat(in.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("socket mode %v, want a socket only the user can use", fi.Mode())
	}
}

func TestSendRefused(t *testing.T) {
	dir := t.TempDir()
	in := acquire(t, dir)
	r := recorder{err: errors.New("Goscriber is quitting")}
	serve(t, in, r.handle)
	if err := send(t, dir, Toggle); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "quitting") {
		t.Errorf("Send: %v, want the app's refusal", err)
	}
	if err := send(t, dir, Command("explode")); err == nil || errors.Is(err, ErrRefused) {
		t.Errorf("Send of an unknown command: %v, want it refused before sending", err)
	}
}

func TestSendWithNoApp(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	if err := send(t, dir, Toggle); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Send to an empty directory: %v, want ErrNotRunning", err)
	}
	// After an app quits, its socket is gone and the lock free.
	in := acquire(t, dir)
	serve(t, in, (&recorder{}).handle)
	path := in.SocketPath()
	if err := in.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket still there after Close: %v", err)
	}
	if err := send(t, dir, Toggle); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Send after Close: %v, want ErrNotRunning", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("finding nothing running took %v", d)
	}
}

// A crash leaves the socket file behind: the command line does not mistake
// it for a running app, and the next launch replaces it.
func TestStaleSocket(t *testing.T) {
	dir := t.TempDir()
	path := socketDir(t, dir)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	ln.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no stale socket to test with: %v", err)
	}

	if err := send(t, dir, Toggle); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Send to a stale socket: %v, want ErrNotRunning", err)
	}
	in := acquire(t, dir)
	var r recorder
	serve(t, in, r.handle)
	if err := send(t, dir, Cancel); err != nil {
		t.Fatalf("Send after the stale socket was replaced: %v", err)
	}
}

// While the app holds the lock but does not listen yet, Send waits for it.
func TestSendWaitsForAStartingApp(t *testing.T) {
	dir := t.TempDir()
	f, err := os.OpenFile(LockPath(dir), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() { result <- send(t, dir, Toggle) }()
	time.Sleep(150 * time.Millisecond)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketDir(t, dir), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var r recorder
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = serveConn(conn, r.handle)
	}()
	if err := <-result; err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := r.got(); len(got) != 1 || got[0] != Toggle {
		t.Errorf("app ran %v", got)
	}
}

// An app that holds the lock and never answers makes Send give up when its
// context ends, with an error that is not ErrNotRunning.
func TestSendGivesUpOnASilentApp(t *testing.T) {
	dir := t.TempDir()
	f, err := os.OpenFile(LockPath(dir), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err = Send(ctx, dir, Toggle)
	if err == nil || errors.Is(err, ErrNotRunning) || !strings.Contains(err.Error(), "does not answer") {
		t.Errorf("Send: %v, want that the app does not answer", err)
	}
}

// Serving ends with its context: the socket goes, the lock stays, so a
// launch while the app shuts down still finds it running.
func TestServeEndsWithItsContextButKeepsTheLock(t *testing.T) {
	dir := t.TempDir()
	in := acquire(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- in.Serve(ctx, (&recorder{}).handle) }()
	if err := send(t, dir, Show); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after its context ended")
	}
	if _, err := os.Stat(in.SocketPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket still there: %v", err)
	}
	if _, err := Acquire(dir); !errors.Is(err, ErrRunning) {
		t.Errorf("Acquire while the lock is held: %v, want ErrRunning", err)
	}
}

// A config directory deep enough that its socket path would not fit in a
// socket address gets a socket in a private directory elsewhere.
func TestLongDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("a-rather-long-directory-name/", 4), "goscriber")
	in := acquire(t, dir)
	var r recorder
	serve(t, in, r.handle)
	path := in.SocketPath()
	if strings.HasPrefix(path, dir) || len(path) > maxSocketPath {
		t.Fatalf("socket %q (%d bytes) did not move out of the long directory", path, len(path))
	}
	t.Cleanup(func() { os.Remove(filepath.Dir(path)) })
	fi, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("socket directory mode %v, want 0700", fi.Mode().Perm())
	}
	if err := send(t, dir, Toggle); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// A socket directory that other users can enter is refused: the app keeps
// the lock and runs without remote control.
func TestOpenSocketDirectoryIsRefused(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", base)
	t.Setenv("TMPDIR", base)
	dir := filepath.Join(t.TempDir(), strings.Repeat("a-rather-long-directory-name/", 4), "goscriber")
	sockDir := filepath.Dir(SocketPath(dir))
	if err := os.MkdirAll(sockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	in := acquire(t, dir)
	if err := in.Serve(context.Background(), (&recorder{}).handle); err == nil || !strings.Contains(err.Error(), "open to other users") {
		t.Errorf("Serve: %v, want the open directory refused", err)
	}
	if _, err := Acquire(dir); !errors.Is(err, ErrRunning) {
		t.Errorf("the lock should still be held: %v", err)
	}
}
