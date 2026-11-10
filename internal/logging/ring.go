package logging

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Entry is one record kept in a Ring.
type Entry struct {
	Time    time.Time
	Level   slog.Level
	Message string
	// Line is the whole record in slog's text format, attributes included,
	// without the trailing newline.
	Line string
}

// Ring keeps the most recent log records in memory for the in-app log viewer.
// It is safe for concurrent use.
type Ring struct {
	mu      sync.Mutex
	entries []Entry
	next    int
	full    bool
	pending Entry // metadata of the record being formatted; guarded by mu
}

// NewRing returns a Ring that keeps the last size records.
func NewRing(size int) *Ring {
	if size < 1 {
		size = 1
	}
	return &Ring{entries: make([]Entry, size)}
}

// Entries returns the kept records, oldest first.
func (r *Ring) Entries() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]Entry(nil), r.entries[:r.next]...)
	}
	out := make([]Entry, 0, len(r.entries))
	out = append(out, r.entries[r.next:]...)
	return append(out, r.entries[:r.next]...)
}

func (r *Ring) push(e Entry) {
	r.entries[r.next] = e
	r.next = (r.next + 1) % len(r.entries)
	if r.next == 0 {
		r.full = true
	}
}

// Handler returns a slog.Handler that records into the ring. Formatting is
// delegated to slog's text handler, so attributes and groups render exactly as
// in the log file.
func (r *Ring) Handler(opts *slog.HandlerOptions) slog.Handler {
	return &ringHandler{ring: r, text: slog.NewTextHandler(ringSink{r}, opts)}
}

type ringHandler struct {
	ring *Ring
	text slog.Handler
}

func (h *ringHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.text.Enabled(ctx, level)
}

// Handle holds the ring lock while the text handler formats the record, so
// ringSink.Write can pair the formatted line with this record's metadata.
func (h *ringHandler) Handle(ctx context.Context, rec slog.Record) error {
	h.ring.mu.Lock()
	defer h.ring.mu.Unlock()
	h.ring.pending = Entry{Time: rec.Time, Level: rec.Level, Message: rec.Message}
	return h.text.Handle(ctx, rec)
}

func (h *ringHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ringHandler{ring: h.ring, text: h.text.WithAttrs(attrs)}
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	return &ringHandler{ring: h.ring, text: h.text.WithGroup(name)}
}

// ringSink receives exactly one Write per record from the text handler, always
// while ringHandler.Handle holds the ring lock.
type ringSink struct{ ring *Ring }

func (s ringSink) Write(p []byte) (int, error) {
	e := s.ring.pending
	e.Line = strings.TrimSuffix(string(p), "\n")
	s.ring.push(e)
	return len(p), nil
}
