package notice

import (
	"log/slog"
	"slices"
	"sync"
	"time"
)

// DefaultQuiet is how long a transient notice stays active after its last
// occurrence: repeats within it are duplicates, and once it has passed the
// next occurrence is news again.
const DefaultQuiet = 2 * time.Minute

// Event is a change to the active notices.
type Event struct {
	// Notice is the notice raised, or the one that ended.
	Notice Notice
	// Ended is false when Notice was raised, true when it was resolved,
	// dismissed or expired.
	Ended bool
	// Active is every active notice after the change, newest first.
	Active []Notice
}

// Clock is the time source of a Hub; tests supply a fake.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f after d and returns a function that cancels the
	// call, reporting whether it did.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }

// Options configure a Hub.
type Options struct {
	// Quiet overrides DefaultQuiet.
	Quiet time.Duration
	// Log receives a line per notice raised. Nil means slog's default
	// logger.
	Log *slog.Logger
	// Clock overrides the real clock.
	Clock Clock
}

// Hub is where background problems are reported. It keeps the active
// notices, one per code, and fans each new one out to every subscriber
// once:
//
//   - A notice whose code is already active is a duplicate: it updates the
//     active notice's message but raises nothing.
//   - A persistent notice (Notice.Persistent: a missing model or
//     permission) stays active until Resolve.
//   - A transient one (a failed paste, an engine crash) also ends on its
//     own once DefaultQuiet passes without a repeat, so the same failure
//     much later is news again.
//
// Publishing never blocks: every subscriber has its own queue. A Hub is
// safe for concurrent use.
type Hub struct {
	quiet time.Duration
	log   *slog.Logger
	clock Clock

	mu     sync.Mutex
	active []*entry // newest first
	subs   map[*subscriber]struct{}
}

type entry struct {
	n     Notice
	count int
	gen   int         // bumped on each (re)arm, so a stale expiry does nothing
	stop  func() bool // cancels the transient notice's expiry
}

// NewHub returns a Hub with no active notices.
func NewHub(opts Options) *Hub {
	h := &Hub{quiet: opts.Quiet, log: opts.Log, clock: opts.Clock, subs: map[*subscriber]struct{}{}}
	if h.quiet <= 0 {
		h.quiet = DefaultQuiet
	}
	if h.log == nil {
		h.log = slog.Default()
	}
	if h.clock == nil {
		h.clock = realClock{}
	}
	return h
}

// Publish reports n. It returns true when n was raised, and false when it
// was a duplicate of an active notice.
func (h *Hub) Publish(n Notice) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := h.find(n.Code); e != nil {
		e.count++
		e.n.Message, e.n.Action = n.Message, n.Action
		h.armLocked(e)
		return false
	}
	n.At = h.clock.Now()
	e := &entry{n: n, count: 1}
	h.active = slices.Insert(h.active, 0, e)
	h.armLocked(e)
	if n.Level == LevelError {
		h.log.Warn("notice", "code", string(n.Code), "source", string(n.Source), "message", n.Message)
	} else {
		h.log.Info("notice", "code", string(n.Code), "source", string(n.Source), "message", n.Message)
	}
	h.emitLocked(Event{Notice: n})
	return true
}

// Resolve ends the active notice with code, when the problem is fixed or
// the user dismisses it. It returns false when none was active.
func (h *Hub) Resolve(code Code) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	i := slices.IndexFunc(h.active, func(e *entry) bool { return e.n.Code == code })
	if i < 0 {
		return false
	}
	h.removeLocked(i)
	return true
}

// Active returns the active notices, newest first.
func (h *Hub) Active() []Notice {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotLocked()
}

// Count returns how many times code was published while it has been active,
// or 0 when it is not active.
func (h *Hub) Count(code Code) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := h.find(code); e != nil {
		return e.count
	}
	return 0
}

// Subscribe returns a channel of every change from now on, in order, and a
// function that ends the subscription and closes the channel. Events wait
// in the subscriber's own queue, so a slow reader holds up nobody.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	s := newSubscriber()
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return s.out, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, s)
			h.mu.Unlock()
			s.close()
		})
	}
}

func (h *Hub) find(code Code) *entry {
	for _, e := range h.active {
		if e.n.Code == code {
			return e
		}
	}
	return nil
}

// armLocked (re)starts a transient notice's expiry.
func (h *Hub) armLocked(e *entry) {
	if e.n.Persistent() {
		return
	}
	if e.stop != nil {
		e.stop()
	}
	e.gen++
	gen := e.gen
	e.stop = h.clock.AfterFunc(h.quiet, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		// The entry may have been resolved, or re-armed by a repeat, since.
		if i := slices.Index(h.active, e); i >= 0 && e.gen == gen {
			h.removeLocked(i)
		}
	})
}

// removeLocked ends the active notice at index i and tells the
// subscribers.
func (h *Hub) removeLocked(i int) {
	e := h.active[i]
	if e.stop != nil {
		e.stop()
	}
	h.active = slices.Delete(h.active, i, i+1)
	h.emitLocked(Event{Notice: e.n, Ended: true})
}

func (h *Hub) snapshotLocked() []Notice {
	out := make([]Notice, len(h.active))
	for i, e := range h.active {
		out[i] = e.n
	}
	return out
}

func (h *Hub) emitLocked(ev Event) {
	ev.Active = h.snapshotLocked()
	for s := range h.subs {
		s.push(ev)
	}
}

// subscriber is an unbounded queue in front of a channel.
type subscriber struct {
	mu     sync.Mutex
	queue  []Event
	closed bool
	wake   chan struct{}
	out    chan Event
}

func newSubscriber() *subscriber {
	s := &subscriber{wake: make(chan struct{}, 1), out: make(chan Event)}
	go s.run()
	return s
}

func (s *subscriber) push(ev Event) {
	s.mu.Lock()
	s.queue = append(s.queue, ev)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *subscriber) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *subscriber) run() {
	defer close(s.out)
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		if len(s.queue) == 0 {
			s.mu.Unlock()
			<-s.wake
			continue
		}
		ev := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
		// Deliver, unless the subscription ends first.
		for delivered := false; !delivered; {
			select {
			case s.out <- ev:
				delivered = true
			case <-s.wake:
				s.mu.Lock()
				closed := s.closed
				s.mu.Unlock()
				if closed {
					return
				}
			}
		}
	}
}
