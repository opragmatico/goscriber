package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	st, rep, err := Open(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Created {
		t.Fatalf("expected a fresh store, got %+v", rep)
	}
	return st
}

func TestStoreUpdatePersists(t *testing.T) {
	st := openStore(t)
	if err := st.Update(func(s *Settings) {
		s.SelectedModel = "nemotron-3.5-asr-streaming-0.6b"
		s.CustomWords = append(s.CustomWords, "Goscriber")
	}); err != nil {
		t.Fatal(err)
	}

	got := st.Get()
	if got.SelectedModel != "nemotron-3.5-asr-streaming-0.6b" || !slices.Equal(got.CustomWords, []string{"Goscriber"}) {
		t.Errorf("Get = %+v", got)
	}
	onDisk, _, err := Load(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(onDisk, got) {
		t.Errorf("file and memory differ:\nfile %+v\n mem %+v", onDisk, got)
	}
}

func TestStoreGetReturnsCopy(t *testing.T) {
	st := openStore(t)
	if err := st.Update(func(s *Settings) { s.CustomWords = []string{"a"} }); err != nil {
		t.Fatal(err)
	}
	s := st.Get()
	s.CustomWords[0] = "mutated"
	if st.Get().CustomWords[0] != "a" {
		t.Error("changing a value from Get changed the store")
	}
}

func TestStoreRejectsInvalidUpdate(t *testing.T) {
	st := openStore(t)
	before := st.Get()
	err := st.Update(func(s *Settings) {
		s.HistoryLimit = 50
		s.OverlayPosition = "middle"
	})
	if !errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("err = %v, want ErrInvalidSettings", err)
	}
	if !reflect.DeepEqual(st.Get(), before) {
		t.Error("a rejected update changed the settings")
	}
	if _, rep, _ := Load(st.Path()); !rep.Created {
		t.Error("a rejected update wrote the settings file")
	}
}

func TestStoreSaveFailureKeepsSettings(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	writeFile(t, blocker, "a file where the settings directory should be")
	st := &Store{path: filepath.Join(blocker, "settings.json"), cur: Defaults(), subs: map[chan Settings]struct{}{}}
	ch, cancel := st.Subscribe()
	defer cancel()

	if err := st.Update(func(s *Settings) { s.HistoryLimit = 9 }); err == nil {
		t.Fatal("expected the save to fail")
	}
	if st.Get().HistoryLimit != Defaults().HistoryLimit {
		t.Error("a failed save changed the in-memory settings")
	}
	select {
	case <-ch:
		t.Error("subscribers were notified of a change that was not saved")
	default:
	}
}

func TestStoreSubscribe(t *testing.T) {
	st := openStore(t)
	ch, cancel := st.Subscribe()
	defer cancel()

	for i := 1; i <= 3; i++ {
		if err := st.Update(func(s *Settings) { s.HistoryLimit = i }); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case got := <-ch:
		if got.HistoryLimit != 3 {
			t.Errorf("subscriber got history_limit %d, want the latest (3)", got.HistoryLimit)
		}
	case <-time.After(time.Second):
		t.Fatal("no notification")
	}
	select {
	case got := <-ch:
		t.Errorf("stale notification left in the channel: %+v", got)
	default:
	}

	cancel()
	cancel() // idempotent
	if _, ok := <-ch; ok {
		t.Error("channel should be closed after cancel")
	}
	if err := st.Update(func(s *Settings) { s.HistoryLimit = 4 }); err != nil {
		t.Errorf("update after unsubscribe: %v", err)
	}
}

func TestStoreConcurrentUpdates(t *testing.T) {
	st := openStore(t)
	ch, cancel := st.Subscribe()
	defer cancel()
	go func() {
		for range ch { // a reader racing with the writers
		}
	}()

	const writers = 32
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			if err := st.Update(func(s *Settings) {
				s.CustomWords = append(s.CustomWords, fmt.Sprintf("word-%02d", i))
			}); err != nil {
				t.Error(err)
			}
			_ = st.Get()
		})
	}
	wg.Wait()

	got := st.Get().CustomWords
	if len(got) != writers {
		t.Fatalf("got %d words, want %d: updates were lost", len(got), writers)
	}
	onDisk, _, err := Load(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(onDisk.CustomWords, got) {
		t.Error("file does not match the final in-memory settings")
	}
}
