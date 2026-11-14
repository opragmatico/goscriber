package pipeline

import (
	"sync"

	"github.com/opragmatico/goscriber/internal/audio"
)

// EventKind says what an Event reports.
type EventKind int

// Event kinds.
const (
	// EventState: the session moved to State.
	EventState EventKind = iota
	// EventRefused: Action was refused, and State is unchanged. Followers
	// that guessed the outcome, such as the shortcut's activator, resync
	// from it.
	EventRefused
	// EventLevel: Level is the microphone's level, about audio.LevelRate
	// times a second while recording. A slow subscriber gets the latest
	// level rather than a backlog.
	EventLevel
	// EventInterim: Text is the transcript so far, while recording, and its
	// first Stable bytes are settled; the rest may still change. A slow
	// subscriber gets the latest transcript rather than a backlog.
	EventInterim
	// EventResult: Text is the transcript that was delivered to the
	// focused app.
	EventResult
)

func (k EventKind) String() string {
	switch k {
	case EventState:
		return "state"
	case EventRefused:
		return "refused"
	case EventLevel:
		return "level"
	case EventInterim:
		return "interim"
	case EventResult:
		return "result"
	default:
		return "unknown"
	}
}

// Event is something the coordinator publishes.
type Event struct {
	Kind EventKind
	// Session is the session the event belongs to; 0 for a refusal outside
	// any session.
	Session uint64
	// State is the state after the event.
	State State
	// Action is the action refused, for EventRefused.
	Action Action
	// Level is the microphone's level, for EventLevel.
	Level audio.Level
	// Text is the transcript, for EventInterim and EventResult.
	Text string
	// Stable is how many bytes at the start of Text are settled, for
	// EventInterim: the words a streaming model will not change, or the
	// chunks an offline model has decoded.
	Stable int
	// Languages are the languages the model detected in the recording, for
	// EventResult; empty when it reports none.
	Languages []string
}

// subscriber is an unbounded queue in front of a channel, so publishing never
// blocks. A level replaces a level still waiting at the end of the queue, and
// an interim replaces the session's interim still waiting since its last
// state change.
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
	if !s.coalesceLocked(ev) {
		s.queue = append(s.queue, ev)
	}
	s.mu.Unlock()
	signal(s.wake)
}

// coalesceLocked puts ev in place of an older event that it supersedes,
// and reports whether it did.
func (s *subscriber) coalesceLocked(ev Event) bool {
	n := len(s.queue)
	switch ev.Kind {
	case EventLevel:
		if n > 0 && s.queue[n-1].Kind == EventLevel {
			s.queue[n-1] = ev
			return true
		}
	case EventInterim:
		for i := n - 1; i >= 0; i-- {
			switch q := s.queue[i]; {
			case q.Kind == EventInterim && q.Session == ev.Session:
				s.queue[i] = ev
				return true
			case q.Kind != EventLevel && q.Kind != EventInterim:
				return false
			}
		}
	}
	return false
}

func (s *subscriber) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	signal(s.wake)
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

// serial runs functions one at a time, in the order posted, on its own
// goroutine. Posting never blocks.
type serial struct {
	mu     sync.Mutex
	queue  []func()
	closed bool
	wake   chan struct{}
	done   chan struct{}
}

func newSerial() *serial {
	s := &serial{wake: make(chan struct{}, 1), done: make(chan struct{})}
	go s.run()
	return s
}

// post queues f. It reports false, and drops f, once the serial is closed.
func (s *serial) post(f func()) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.queue = append(s.queue, f)
	s.mu.Unlock()
	signal(s.wake)
	return true
}

// close runs what is queued, then stops, and waits for that.
func (s *serial) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	signal(s.wake)
	<-s.done
}

func (s *serial) run() {
	defer close(s.done)
	for {
		s.mu.Lock()
		queue, closed := s.queue, s.closed
		s.queue = nil
		s.mu.Unlock()
		for _, f := range queue {
			f()
		}
		if len(queue) > 0 {
			continue
		}
		if closed {
			return
		}
		<-s.wake
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
