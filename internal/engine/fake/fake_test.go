package fake

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/engine"
)

var spec = engine.LoadSpec{ID: "nemotron-3.5", Path: "/models/nemotron.gguf"}

// second is one second of silence at the fake's model rate.
var second = make([]float32, SampleRate)

func load(t *testing.T, s Script) *Engine {
	t.Helper()
	e := New(s)
	if _, err := e.Load(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	return e
}

func wantKind(t *testing.T, err, kind error) {
	t.Helper()
	if !errors.Is(err, kind) {
		t.Fatalf("err = %v, want %v", err, kind)
	}
	if _, ok := errors.AsType[*engine.Error](err); !ok {
		t.Fatalf("err = %T, want *engine.Error", err)
	}
}

func TestLoadReportsModel(t *testing.T) {
	e := New(Script{Model: engine.ModelInfo{Streaming: true, Languages: []string{"auto", "en-US"}}})
	if e.Loaded() {
		t.Fatal("loaded before Load")
	}
	info, err := e.Load(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != spec.ID || !info.Streaming || !slices.Equal(info.Languages, []string{"auto", "en-US"}) {
		t.Errorf("Load = %+v", info)
	}
	if info.DecodeMode() != engine.DecodeLive {
		t.Errorf("DecodeMode = %v", info.DecodeMode())
	}
	if !e.Loaded() || !slices.Equal(e.Loads(), []engine.LoadSpec{spec}) {
		t.Errorf("Loaded = %v, Loads = %v", e.Loaded(), e.Loads())
	}

	info, err = New(Script{Model: engine.ModelInfo{ID: "scripted"}}).Load(context.Background(), spec)
	if err != nil || info.ID != "scripted" {
		t.Errorf("Load with a scripted ID = %+v, %v", info, err)
	}
}

func TestLoadErrorLeavesNothingLoaded(t *testing.T) {
	e := New(Script{LoadErr: engine.ErrOutOfMemory})
	_, err := e.Load(context.Background(), spec)
	wantKind(t, err, engine.ErrOutOfMemory)
	if e.Loaded() {
		t.Error("loaded after a failed Load")
	}
	_, err = e.Recognize(context.Background(), second, 0, engine.Options{})
	wantKind(t, err, engine.ErrNotLoaded)
}

func TestLoadDelayAndCancel(t *testing.T) {
	e := New(Script{LoadDelay: time.Hour})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := e.Load(ctx, spec)
	wantKind(t, err, engine.ErrCancelled)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to wrap the context's error", err)
	}
	if e.Loaded() {
		t.Error("loaded after a cancelled Load")
	}
}

func TestLoadReplacesAndUnload(t *testing.T) {
	e := load(t, Script{Model: engine.ModelInfo{Streaming: true}})
	s, err := e.StartStream(context.Background(), engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Load(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if e.Unloads() != 1 {
		t.Errorf("Unloads = %d after a replacing Load, want 1", e.Unloads())
	}
	if !s.(*Stream).Closed() {
		t.Error("the old model's stream is still open")
	}
	wantKind(t, s.Push(second, 0), engine.ErrInvalidArgument)

	if err := e.Unload(); err != nil {
		t.Fatal(err)
	}
	if err := e.Unload(); err != nil {
		t.Fatal(err)
	}
	if e.Loaded() || e.Unloads() != 2 {
		t.Errorf("Loaded = %v, Unloads = %d; want false, 2", e.Loaded(), e.Unloads())
	}
}

func TestRecognizeRepliesInOrder(t *testing.T) {
	e := load(t, Script{Replies: []Reply{Transcript("one"), Transcript("two")}})
	opts := engine.Options{Language: "en-US", WordTimestamps: true, Phrases: []string{"Goscriber"}}
	var got []string
	for range 3 {
		r, err := e.Recognize(context.Background(), second, 0, opts)
		if err != nil {
			t.Fatal(err)
		}
		if !r.Final || r.AudioProcessed != time.Second {
			t.Errorf("result = %+v, want final over 1s", r)
		}
		got = append(got, r.Text)
	}
	if !slices.Equal(got, []string{"one", "two", "two"}) {
		t.Errorf("transcripts = %q, the last reply should repeat", got)
	}

	recs := e.Recognitions()
	if len(recs) != 3 || recs[0].Samples != SampleRate || recs[0].SampleRate != 0 || recs[0].Options.Language != "en-US" {
		t.Fatalf("Recognitions = %+v", recs)
	}
	opts.Phrases[0] = "mutated"
	if recs[0].Options.Phrases[0] != "Goscriber" {
		t.Error("the recorded options share the caller's slice")
	}
}

func TestRecognizeWithoutReplies(t *testing.T) {
	e := load(t, Script{})
	r, err := e.Recognize(context.Background(), make([]float32, 48000), 48000, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "" || !r.Final || r.AudioProcessed != time.Second {
		t.Errorf("result = %+v", r)
	}
}

func TestRecognizeErrors(t *testing.T) {
	scripted := &engine.Error{Op: "recognize", Kind: engine.ErrRuntime, Detail: "GGML_ASSERT"}
	e := load(t, Script{Replies: []Reply{{Err: engine.ErrResourceExhausted}, {Err: scripted}}})

	_, err := e.Recognize(context.Background(), second, 0, engine.Options{})
	wantKind(t, err, engine.ErrResourceExhausted)
	if _, err := e.Recognize(context.Background(), second, 0, engine.Options{}); err != scripted {
		t.Errorf("err = %v, want the scripted *engine.Error itself", err)
	}

	for _, tc := range []struct {
		samples []float32
		rate    int
	}{
		{nil, 0},
		{second, 4000},
		{second, 192000},
	} {
		_, err := e.Recognize(context.Background(), tc.samples, tc.rate, engine.Options{})
		wantKind(t, err, engine.ErrInvalidArgument)
	}
	if n := len(e.Recognitions()); n != 2 {
		t.Errorf("Recognitions = %d, invalid calls should not reach the model", n)
	}
}

func TestRecognizeDelayAndCancel(t *testing.T) {
	e := load(t, Script{Replies: []Reply{{Result: engine.Result{Text: "slow"}, Delay: 20 * time.Millisecond}}})
	start := time.Now()
	r, err := e.Recognize(context.Background(), second, 0, engine.Options{})
	if err != nil || r.Text != "slow" {
		t.Fatalf("Recognize = %+v, %v", r, err)
	}
	if time.Since(start) < 20*time.Millisecond {
		t.Error("the delay was not applied")
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(5 * time.Millisecond); cancel() }()
	e = load(t, Script{Replies: []Reply{{Delay: time.Hour}}})
	_, err = e.Recognize(ctx, second, 0, engine.Options{})
	wantKind(t, err, engine.ErrCancelled)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to wrap context.Canceled", err)
	}

	_, err = e.Recognize(ctx, second, 0, engine.Options{})
	wantKind(t, err, engine.ErrCancelled)
}

func TestUnloadWaitsForCallsInFlight(t *testing.T) {
	e := load(t, Script{Replies: []Reply{{Delay: 50 * time.Millisecond}}})
	started := make(chan struct{})
	done := make(chan error)
	go func() {
		close(started)
		_, err := e.Recognize(context.Background(), second, 0, engine.Options{})
		done <- err
	}()
	<-started
	time.Sleep(10 * time.Millisecond)
	start := time.Now()
	if err := e.Unload(); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < 30*time.Millisecond {
		t.Errorf("Unload returned after %v, before the call in flight", waited)
	}
	if err := <-done; err != nil {
		t.Errorf("the call in flight failed: %v", err)
	}
}

func TestStartStreamRules(t *testing.T) {
	e := New(Script{})
	_, err := e.StartStream(context.Background(), engine.Options{})
	wantKind(t, err, engine.ErrNotLoaded)

	e = load(t, Script{})
	_, err = e.StartStream(context.Background(), engine.Options{})
	wantKind(t, err, engine.ErrInvalidArgument)

	e = load(t, Script{Model: engine.ModelInfo{Streaming: true}, Streams: []StreamScript{{StartErr: engine.ErrUnavailable}}})
	_, err = e.StartStream(context.Background(), engine.Options{})
	wantKind(t, err, engine.ErrUnavailable)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = e.StartStream(ctx, engine.Options{})
	wantKind(t, err, engine.ErrCancelled)
}

// drain returns the results Next has now, failing the test on errors.
func drain(t *testing.T, s engine.Stream) []engine.Result {
	t.Helper()
	var out []engine.Result
	for {
		r, err := s.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if r == nil {
			return out
		}
		out = append(out, *r)
	}
}

func texts(rs []engine.Result) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Text)
	}
	return out
}

func startStream(t *testing.T, script StreamScript, opts engine.Options) (*Engine, engine.Stream) {
	t.Helper()
	e := load(t, Script{Model: engine.ModelInfo{Streaming: true}, Streams: []StreamScript{script}})
	s, err := e.StartStream(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return e, s
}

func TestStreamInterimsFollowTheAudio(t *testing.T) {
	e, s := startStream(t, Dictation("ask not what your country", 200*time.Millisecond), engine.Options{InterimResults: true})
	defer s.Close()

	if got := drain(t, s); got != nil {
		t.Fatalf("results before any audio: %v", got)
	}
	// 20 ms chunks at 48 kHz: 10 chunks are 200 ms, one word.
	chunk := make([]float32, 960)
	var got []string
	for range 25 {
		if err := s.Push(chunk, 48000); err != nil {
			t.Fatal(err)
		}
		for _, r := range drain(t, s) {
			if r.Final {
				t.Errorf("final %q before Finish", r.Text)
			}
			got = append(got, r.Text)
		}
	}
	want := []string{"ask", "ask not"}
	if !slices.Equal(got, want) {
		t.Errorf("interims after 500 ms = %q, want %q", got, want)
	}

	if err := s.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := s.Next()
	if err != nil || r == nil {
		t.Fatalf("Next after Finish = %v, %v", r, err)
	}
	if !r.Final || r.Text != "ask not what your country" || r.AudioProcessed != 500*time.Millisecond {
		t.Errorf("end-of-stream final = %+v", r)
	}
	if _, err := s.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("Next after the final = %v, want io.EOF", err)
	}

	fs := e.Streams()[0]
	if fs.Audio() != 500*time.Millisecond || fs.SampleRate() != 48000 || !fs.Finished() || !fs.Options().InterimResults {
		t.Errorf("recorded stream: audio %v, rate %d, finished %v", fs.Audio(), fs.SampleRate(), fs.Finished())
	}
}

func TestStreamWithoutInterimResults(t *testing.T) {
	script := Dictation("ask not", 100*time.Millisecond)
	script.Results = append(script.Results, engine.Result{Text: "endpointed", Final: true, AudioProcessed: 300 * time.Millisecond})
	_, s := startStream(t, script, engine.Options{})
	if err := s.Push(second, 0); err != nil {
		t.Fatal(err)
	}
	if got := texts(drain(t, s)); !slices.Equal(got, []string{"endpointed"}) {
		t.Errorf("results = %q, want only the scripted final", got)
	}
}

func TestStreamForceEndpoint(t *testing.T) {
	_, s := startStream(t, Dictation("ask not what", 100*time.Millisecond), engine.Options{InterimResults: true})
	if err := s.Push(make([]float32, SampleRate/5), 0); err != nil {
		t.Fatal(err)
	}
	drain(t, s)
	if err := s.ForceEndpoint(); err != nil {
		t.Fatal(err)
	}
	r, err := s.Next()
	if err != nil || r == nil || !r.Final || r.Text != "ask not" {
		t.Fatalf("Next after ForceEndpoint = %+v, %v; want final \"ask not\"", r, err)
	}
	if got := drain(t, s); got != nil {
		t.Errorf("results after the endpoint = %v", got)
	}
}

func TestStreamFinishDropsUnreachedResults(t *testing.T) {
	_, s := startStream(t, Dictation("ask not what", time.Second), engine.Options{InterimResults: true})
	if err := s.Push(make([]float32, SampleRate/2), 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := s.Next()
	if err != nil || !r.Final || r.Text != "ask not what" {
		t.Errorf("Next = %+v, %v; want the end-of-stream final", r, err)
	}
}

func TestStreamMisuse(t *testing.T) {
	_, s := startStream(t, StreamScript{}, engine.Options{})

	if err := s.Push(nil, 0); err != nil {
		t.Errorf("empty Push = %v", err)
	}
	wantKind(t, s.Push(second, 1000), engine.ErrInvalidArgument)
	if err := s.Push(second, 16000); err != nil {
		t.Fatal(err)
	}
	wantKind(t, s.Push(second, 48000), engine.ErrInvalidArgument)

	if err := s.Finish(); err != nil {
		t.Fatal(err)
	}
	wantKind(t, s.Push(second, 16000), engine.ErrInvalidArgument)
	wantKind(t, s.ForceEndpoint(), engine.ErrInvalidArgument)
	wantKind(t, s.Finish(), engine.ErrInvalidArgument)

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close = %v", err)
	}
	_, err := s.Next()
	wantKind(t, err, engine.ErrInvalidArgument)
}

func TestStreamScriptedErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		script StreamScript
		call   func(engine.Stream) error
	}{
		"push":   {StreamScript{PushErr: engine.ErrRuntime}, func(s engine.Stream) error { return s.Push(second, 0) }},
		"next":   {StreamScript{NextErr: engine.ErrRuntime}, func(s engine.Stream) error { _, err := s.Next(); return err }},
		"finish": {StreamScript{FinishErr: engine.ErrRuntime}, func(s engine.Stream) error { return s.Finish() }},
	} {
		t.Run(name, func(t *testing.T) {
			_, s := startStream(t, tc.script, engine.Options{})
			wantKind(t, tc.call(s), engine.ErrRuntime)
		})
	}
}

func TestStreamFinishDelay(t *testing.T) {
	_, s := startStream(t, StreamScript{FinishDelay: 20 * time.Millisecond}, engine.Options{})
	start := time.Now()
	if err := s.Finish(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 20*time.Millisecond {
		t.Error("the finish delay was not applied")
	}
}

// Run with -race: recognitions and streams from several goroutines while the
// model is reloaded.
func TestConcurrentUse(t *testing.T) {
	e := load(t, Script{
		Model:   engine.ModelInfo{Streaming: true},
		Replies: []Reply{Transcript("hello")},
		Streams: []StreamScript{Dictation("hello world", 10*time.Millisecond)},
	})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				_, _ = e.Recognize(context.Background(), second[:1600], 0, engine.Options{})
				s, err := e.StartStream(context.Background(), engine.Options{InterimResults: true})
				if err != nil {
					continue // the model was being reloaded
				}
				_ = s.Push(second[:1600], 0)
				_, _ = s.Next()
				_ = s.Finish()
				_, _ = s.Next()
				_ = s.Close()
			}
		})
	}
	wg.Go(func() {
		for range 5 {
			_, _ = e.Load(context.Background(), spec)
			_ = e.Unload()
		}
	})
	wg.Wait()
}
