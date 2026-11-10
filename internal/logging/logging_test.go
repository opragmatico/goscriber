package logging

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newTestLogger(t *testing.T, opts Options) *Logger {
	t.Helper()
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	l, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWritesFileStderrAndRing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	var stderr bytes.Buffer
	l := newTestLogger(t, Options{Dir: dir, Level: slog.LevelInfo, Stderr: &stderr})

	l.Info("hello", "model", "nemotron")
	l.Debug("hidden at info")

	if got, want := l.FilePath(), filepath.Join(dir, FileName); got != want {
		t.Fatalf("FilePath = %s, want %s", got, want)
	}
	for name, out := range map[string]string{"file": readFile(t, l.FilePath()), "stderr": stderr.String()} {
		if !strings.Contains(out, "msg=hello model=nemotron") {
			t.Errorf("%s is missing the record: %q", name, out)
		}
		if strings.Contains(out, "hidden at info") {
			t.Errorf("%s has a record below the level: %q", name, out)
		}
	}
	entries := l.Ring().Entries()
	if len(entries) != 1 || entries[0].Message != "hello" || entries[0].Level != slog.LevelInfo ||
		!strings.Contains(entries[0].Line, "model=nemotron") {
		t.Errorf("ring = %+v", entries)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Errorf("log dir should be private: %v %v", info.Mode(), err)
	}
}

func TestSetLevelAppliesAtRuntime(t *testing.T) {
	l := newTestLogger(t, Options{Dir: t.TempDir(), Level: slog.LevelWarn})
	l.Info("before")
	l.SetLevel(slog.LevelDebug)
	l.Debug("after")
	if l.Level() != slog.LevelDebug {
		t.Errorf("Level = %v", l.Level())
	}
	out := readFile(t, l.FilePath())
	if strings.Contains(out, "before") || !strings.Contains(out, "msg=after") {
		t.Errorf("level change not applied: %q", out)
	}
}

func TestNoFileWithoutDir(t *testing.T) {
	l := newTestLogger(t, Options{})
	l.Info("ring only")
	if l.FilePath() != "" || len(l.Ring().Entries()) != 1 {
		t.Errorf("FilePath %q, ring %v", l.FilePath(), l.Ring().Entries())
	}
	if err := l.Close(); err != nil {
		t.Error(err)
	}
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, Options{Dir: dir, Level: slog.LevelInfo, MaxFileSize: 512, MaxFiles: 3})
	for i := range 100 {
		l.Info("record", "n", i)
	}
	path := filepath.Join(dir, FileName)
	for _, name := range []string{path, path + ".1", path + ".2"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Size() > 512 {
			t.Errorf("%s is %d bytes, over the 512-byte limit", name, info.Size())
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("more than MaxFiles files kept: %v", err)
	}
	if !strings.Contains(readFile(t, path), "n=99") {
		t.Error("the newest record should be in the current file")
	}
	if strings.Contains(readFile(t, path+".1"), "n=99") {
		t.Error("rotated file holds the newest record")
	}
}

func TestReopenAppendsToExistingFile(t *testing.T) {
	dir := t.TempDir()
	first, err := New(Options{Dir: dir, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	first.Info("first run")
	first.Close()
	first.Info("after close") // must not panic; the file is closed

	second := newTestLogger(t, Options{Dir: dir})
	second.Info("second run")
	out := readFile(t, second.FilePath())
	if !strings.Contains(out, "first run") || !strings.Contains(out, "second run") {
		t.Errorf("log file was not appended to: %q", out)
	}
}

func TestRingKeepsNewestInOrder(t *testing.T) {
	l := newTestLogger(t, Options{RingSize: 3, Level: slog.LevelDebug})
	for i := range 5 {
		l.Info(fmt.Sprintf("m%d", i))
	}
	var got []string
	for _, e := range l.Ring().Entries() {
		got = append(got, e.Message)
	}
	if strings.Join(got, ",") != "m2,m3,m4" {
		t.Errorf("ring = %v, want the last three oldest first", got)
	}
}

func TestRingKeepsAttrsAndGroups(t *testing.T) {
	l := newTestLogger(t, Options{Level: slog.LevelDebug})
	l.With("component", "engine").WithGroup("job").Warn("slow", "ms", 900)
	e := l.Ring().Entries()
	if len(e) != 1 || !strings.Contains(e[0].Line, "component=engine") || !strings.Contains(e[0].Line, "job.ms=900") ||
		e[0].Level != slog.LevelWarn {
		t.Errorf("ring entry = %+v", e)
	}
}

func TestConcurrentLogging(t *testing.T) {
	l := newTestLogger(t, Options{Dir: t.TempDir(), Level: slog.LevelInfo, MaxFileSize: 4096, RingSize: 50})
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			lg := l.With("goroutine", g)
			for i := range 200 {
				lg.Info("tick", "i", i)
				if i%50 == 0 {
					_ = l.Ring().Entries()
				}
			}
		})
	}
	wg.Wait()
	entries := l.Ring().Entries()
	if len(entries) != 50 {
		t.Fatalf("ring has %d entries, want 50", len(entries))
	}
	for _, e := range entries {
		if e.Message != "tick" || !strings.Contains(e.Line, "msg=tick") {
			t.Fatalf("ring entry mixed up line and metadata: %+v", e)
		}
	}
}

func TestEffectiveLevel(t *testing.T) {
	tests := []struct {
		setting string
		debug   bool
		env     string
		want    slog.Level
	}{
		{"warn", false, "", slog.LevelWarn},
		{"warn", true, "error", slog.LevelDebug},
		{"warn", false, "ERROR", slog.LevelError},
		{"warn", false, "loud", slog.LevelWarn},
		{"", false, "", slog.LevelInfo},
		{"chatty", false, "", slog.LevelInfo},
	}
	for _, tt := range tests {
		if got := EffectiveLevel(tt.setting, tt.debug, tt.env); got != tt.want {
			t.Errorf("EffectiveLevel(%q, %v, %q) = %v, want %v", tt.setting, tt.debug, tt.env, got, tt.want)
		}
	}
}
