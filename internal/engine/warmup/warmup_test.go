package warmup

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/fake"
)

var spec = engine.LoadSpec{ID: "asr", Path: "/models/asr.gguf"}

// longest is the longest clip the tests hand an offline model.
const longest = 25 * time.Second

func newEngine(s fake.Script) (*Engine, *fake.Engine, *bytes.Buffer) {
	inner := fake.New(s)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return New(inner, Options{Longest: longest, Log: log}), inner, &logs
}

// An offline model decodes the longest clip of silence once, whole, at the
// models' rate, before Load returns; later calls go straight through.
func TestLoadWarmsAnOfflineModel(t *testing.T) {
	e, inner, logs := newEngine(fake.Script{Replies: []fake.Reply{fake.Transcript(""), fake.Transcript("hello")}})
	info, err := e.Load(context.Background(), spec)
	if err != nil || info.ID != "asr" {
		t.Fatalf("Load = %+v, %v", info, err)
	}
	recs := inner.Recognitions()
	if len(recs) != 1 || recs[0].SampleRate != 16000 || recs[0].Samples != 25*16000 || recs[0].Options.Language != "" {
		t.Fatalf("recognitions after Load %+v, want 25 s of silence at 16 kHz", recs)
	}
	if n := len(inner.Streams()); n != 0 {
		t.Errorf("%d streams for an offline model", n)
	}
	if !strings.Contains(logs.String(), "model warmed up") {
		t.Errorf("no warm-up log line: %s", logs)
	}

	r, err := e.Recognize(context.Background(), make([]float32, 16000), 16000, engine.Options{Language: "en-US"})
	if err != nil || r.Text != "hello" {
		t.Errorf("Recognize = %+v, %v", r, err)
	}
	if n := len(inner.Recognitions()); n != 2 {
		t.Errorf("%d recognitions, want the warm-up and the call", n)
	}
}

// A streaming model decodes half a second of silence whole, then streams
// as much, to the end, and closes the stream.
func TestLoadWarmsAStreamingModel(t *testing.T) {
	e, inner, _ := newEngine(fake.Script{
		Model:   engine.ModelInfo{Streaming: true},
		Streams: []fake.StreamScript{fake.Dictation("", 0)},
	})
	if _, err := e.Load(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if recs := inner.Recognitions(); len(recs) != 1 || recs[0].Samples != 8000 {
		t.Errorf("recognitions %+v, want half a second of silence", recs)
	}
	streams := inner.Streams()
	if len(streams) != 1 {
		t.Fatalf("%d streams, want one", len(streams))
	}
	s := streams[0]
	if s.Audio() != Silence || s.SampleRate() != 16000 || !s.Finished() || !s.Closed() {
		t.Errorf("stream: audio %v at %d Hz, finished %v, closed %v", s.Audio(), s.SampleRate(), s.Finished(), s.Closed())
	}
}

// With no longest clip given, an offline model gets the shortest warm-up.
func TestShortestWarmUp(t *testing.T) {
	inner := fake.New(fake.Script{})
	if _, err := New(inner, Options{Log: slog.New(slog.DiscardHandler)}).Load(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if recs := inner.Recognitions(); len(recs) != 1 || recs[0].Samples != 8000 {
		t.Errorf("recognitions %+v, want half a second of silence", recs)
	}
}

// A failed load is returned as it is, with no warm-up.
func TestFailedLoadIsNotWarmed(t *testing.T) {
	e, inner, _ := newEngine(fake.Script{LoadErr: engine.ErrModelNotFound})
	if _, err := e.Load(context.Background(), spec); !errors.Is(err, engine.ErrModelNotFound) {
		t.Fatalf("Load = %v", err)
	}
	if n := len(inner.Recognitions()); n != 0 {
		t.Errorf("%d recognitions after a failed load", n)
	}
}

// A warm-up that fails leaves the model loaded and only logs.
func TestFailedWarmUpKeepsTheModel(t *testing.T) {
	for _, s := range []fake.Script{
		{Replies: []fake.Reply{{Err: engine.ErrRuntime}}},
		{Model: engine.ModelInfo{Streaming: true}, Streams: []fake.StreamScript{{FinishErr: engine.ErrRuntime}}},
		{Model: engine.ModelInfo{Streaming: true}, Streams: []fake.StreamScript{{StartErr: engine.ErrRuntime}}},
	} {
		e, inner, logs := newEngine(s)
		if _, err := e.Load(context.Background(), spec); err != nil {
			t.Fatalf("Load = %v, want the warm-up's failure kept out of it", err)
		}
		if !inner.Loaded() {
			t.Error("the model should stay loaded")
		}
		if !strings.Contains(logs.String(), "warming up the model failed") {
			t.Errorf("no warning: %s", logs)
		}
		for _, st := range inner.Streams() {
			if !st.Closed() {
				t.Error("the warm-up stream was left open")
			}
		}
	}
}

// Cancelling the load's context during the warm-up cuts it short; the model
// stays loaded.
func TestCancelCutsTheWarmUpShort(t *testing.T) {
	e, inner, logs := newEngine(fake.Script{Replies: []fake.Reply{{Delay: time.Minute}}})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	start := time.Now()
	if _, err := e.Load(ctx, spec); err != nil {
		t.Fatalf("Load = %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Load took %v after the cancel", took)
	}
	if !inner.Loaded() {
		t.Error("the model should stay loaded")
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("a cancelled warm-up is not a failure: %s", logs)
	}
}
