package config

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ErrInvalidSettings is returned (wrapped) by Store.Update when the change
// leaves a field with an invalid value. Nothing is saved in that case.
var ErrInvalidSettings = errors.New("invalid settings")

// Store owns the settings for the running app: it loads them once, hands out
// copies, persists every change and tells subscribers about it. It is safe for
// concurrent use.
type Store struct {
	path string

	mu   sync.Mutex
	cur  Settings
	subs map[chan Settings]struct{}
}

// Open loads the settings at path (see Load) and returns a Store for them.
func Open(path string) (*Store, LoadReport, error) {
	s, rep, err := Load(path)
	if err != nil {
		return nil, rep, err
	}
	return &Store{path: path, cur: s, subs: map[chan Settings]struct{}{}}, rep, nil
}

// Path is the settings file the store reads and writes.
func (st *Store) Path() string { return st.path }

// Get returns a copy of the current settings.
func (st *Store) Get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cur.Clone()
}

// Update applies fn to a copy of the current settings, validates the result,
// saves it and only then makes it current and notifies subscribers. Updates are
// serialised, so fn always sees the latest settings.
func (st *Store) Update(fn func(*Settings)) error {
	st.mu.Lock()
	defer st.mu.Unlock()

	next := st.cur.Clone()
	fn(&next)
	next.SchemaVersion = SchemaVersion
	if fixed := next.normalize(Defaults()); len(fixed) > 0 {
		return fmt.Errorf("config: %w: %s", ErrInvalidSettings, strings.Join(fixed, ", "))
	}
	if err := Save(st.path, next); err != nil {
		return err
	}
	st.cur = next
	for ch := range st.subs {
		deliverLatest(ch, next.Clone())
	}
	return nil
}

// Subscribe returns a channel that receives the settings after every
// successful Update, and a function that stops the subscription and closes the
// channel. A slow reader only ever sees the latest value: older undelivered
// values are dropped.
func (st *Store) Subscribe() (<-chan Settings, func()) {
	ch := make(chan Settings, 1)
	st.mu.Lock()
	st.subs[ch] = struct{}{}
	st.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			st.mu.Lock()
			delete(st.subs, ch)
			st.mu.Unlock()
			close(ch)
		})
	}
}

// deliverLatest replaces any undelivered value in ch with v. Callers hold the
// store lock, so there is only ever one sender.
func deliverLatest(ch chan Settings, v Settings) {
	select {
	case ch <- v:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- v:
	default:
	}
}
