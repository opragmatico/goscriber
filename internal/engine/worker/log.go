package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
)

// The worker's stderr carries two kinds of lines: its own log, which
// NewWorkerLogger writes as JSON, and whatever NeMo-Speech.cpp and ggml
// print, such as "[asr] model=… backend=MTL0". The supervisor re-logs both
// through its own logger, with the worker's process ID as the "worker"
// attribute, so they land in Goscriber's log file at the level the
// log_level setting allows.

// NewWorkerLogger is the logger a worker process writes its own log with:
// JSON lines on w (its stderr) at every level, which the supervisor
// filters.
func NewWorkerLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// stderrTail is how many of the worker's last stderr lines a crash report
// shows: an assertion's message is printed right before the abort.
const stderrTail = 20

// forwardLog re-logs each line the worker writes to r until r ends, and
// keeps the last ones in t.
func forwardLog(r io.Reader, log *slog.Logger, t *tail) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		t.add(line)
		relog(log, line)
	}
	if err := sc.Err(); err != nil {
		log.Debug("reading the engine worker's log stopped", "err", err)
		_, _ = io.Copy(io.Discard, r)
	}
}

// relog logs one line of the worker's stderr. A JSON line from the
// worker's own logger keeps its level, message and attributes; any other
// line is the native engine's, logged at debug level, or as a warning when
// it reads like a failure.
func relog(log *slog.Logger, line string) {
	ctx := context.Background()
	if strings.HasPrefix(line, "{") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil {
			if msg, ok := rec[slog.MessageKey].(string); ok {
				var level slog.Level
				if s, ok := rec[slog.LevelKey].(string); ok {
					_ = level.UnmarshalText([]byte(s))
				}
				delete(rec, slog.MessageKey)
				delete(rec, slog.LevelKey)
				delete(rec, slog.TimeKey)
				keys := make([]string, 0, len(rec))
				for k := range rec {
					keys = append(keys, k)
				}
				slices.Sort(keys)
				args := make([]any, 0, 2*len(keys))
				for _, k := range keys {
					args = append(args, k, rec[k])
				}
				log.Log(ctx, level, msg, args...)
				return
			}
		}
	}
	level := slog.LevelDebug
	if looksLikeFailure(line) {
		level = slog.LevelWarn
	}
	log.Log(ctx, level, "engine output", "line", line)
}

func looksLikeFailure(line string) bool {
	l := strings.ToLower(line)
	for _, w := range []string{"error", "assert", "fatal", "abort", "panic", "failed"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// tail keeps the last lines written to it.
type tail struct {
	max   int
	mu    sync.Mutex
	lines []string
}

func (t *tail) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == t.max {
		t.lines = append(t.lines[:0], t.lines[1:]...)
	}
	t.lines = append(t.lines, line)
}

func (t *tail) get() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.lines)
}
