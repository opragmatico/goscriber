package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/fake"
)

// progressLog records what Transcribe reports.
type progressLog struct {
	mu    sync.Mutex
	done  []time.Duration
	total time.Duration
}

func (p *progressLog) report(done, total time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done = append(p.done, done)
	p.total = total
}

// check fails the test unless progress rose to total, once per slice.
func (p *progressLog) check(t *testing.T, total time.Duration) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	slices := int((total + transcribeSlice - 1) / transcribeSlice)
	if len(p.done) != slices || p.total != total {
		t.Fatalf("progress %v of %v, want %d reports up to %v", p.done, p.total, slices, total)
	}
	for i := 1; i < len(p.done); i++ {
		if p.done[i] <= p.done[i-1] {
			t.Fatalf("progress went back: %v", p.done)
		}
	}
	if last := p.done[len(p.done)-1]; last != total {
		t.Errorf("progress ended at %v of %v", last, total)
	}
}

// A long file for an offline model is decoded in chunks cut at its pauses,
// whatever long_form_chunking says, with progress along the way.
func TestTranscribeOfflineInChunks(t *testing.T) {
	parts := [][]float32{speech(8 * time.Second)}
	for range 8 {
		parts = append(parts, pause(time.Second), speech(8*time.Second))
	}
	samples := concat(parts...)
	r := newRig(t, rigOptions{
		script:   fake.Script{Replies: []fake.Reply{fake.Transcript("words")}},
		settings: func(s *config.Settings) { s.LongFormChunking = false },
	})
	var p progressLog
	got, err := r.c.Transcribe(context.Background(), samples, rate, TranscribeOptions{Progress: p.report})
	if err != nil {
		t.Fatal(err)
	}
	total := time.Duration(len(samples)) * time.Second / rate
	p.check(t, total)

	recs := r.eng.Recognitions()
	if len(recs) < 3 {
		t.Fatalf("%d recognitions of %v of audio, want it decoded in chunks", len(recs), total)
	}
	sum := 0
	for _, rec := range recs {
		if rec.Samples > at(defaultChunkPlan.maxChunk) {
			t.Errorf("a chunk of %d samples, longer than %v", rec.Samples, defaultChunkPlan.maxChunk)
		}
		sum += rec.Samples
	}
	if sum != len(samples) {
		t.Errorf("chunks add up to %d samples of %d", sum, len(samples))
	}
	if words := strings.Fields(got.RawText); len(words) != len(recs) || got.Model != "asr" {
		t.Errorf("got %+v from %d chunks", got, len(recs))
	}
	if len(r.out.delivered()) != 0 || len(r.events.states()) != 0 {
		t.Error("a transcription delivers nothing and leaves dictation idle")
	}
}

// A streaming model gets the whole file through one stream.
func TestTranscribeThroughAStream(t *testing.T) {
	samples := speech(25 * time.Second)
	r := newRig(t, rigOptions{script: streaming(fake.StreamScript{Final: engine.Result{Text: "streamed words"}})})
	var p progressLog
	got, err := r.c.Transcribe(context.Background(), samples, rate, TranscribeOptions{Progress: p.report})
	if err != nil {
		t.Fatal(err)
	}
	if got.RawText != "streamed words" {
		t.Errorf("got %+v", got)
	}
	p.check(t, 25*time.Second)
	streams := r.eng.Streams()
	if len(streams) != 1 || streams[0].Audio() != 25*time.Second || !streams[0].Finished() {
		t.Fatalf("streams %+v, want one fed the whole file and finished", streams)
	}
	if recs := r.eng.Recognitions(); len(recs) != 0 {
		t.Errorf("%d whole-clip recognitions besides the stream", len(recs))
	}
	waitFor(t, "the stream to close", streams[0].Closed)
}

// Cancelling stops the decode between slices and closes the stream.
func TestTranscribeCancel(t *testing.T) {
	samples := speech(60 * time.Second)
	r := newRig(t, rigOptions{script: streaming(fake.StreamScript{Final: engine.Result{Text: "never"}})})
	ctx, cancel := context.WithCancel(context.Background())
	_, err := r.c.Transcribe(ctx, samples, rate, TranscribeOptions{Progress: func(done, _ time.Duration) {
		if done >= 2*transcribeSlice {
			cancel()
		}
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	streams := r.eng.Streams()
	if len(streams) != 1 {
		t.Fatalf("%d streams", len(streams))
	}
	waitFor(t, "the stream to close", streams[0].Closed)
	if streams[0].Finished() || streams[0].Audio() >= 60*time.Second {
		t.Errorf("the cancelled stream was fed %v and finished: %v", streams[0].Audio(), streams[0].Finished())
	}
}

// A dictation started during a long transcription runs in between its
// slices, and both complete.
func TestTranscribeAndADictationInterleave(t *testing.T) {
	samples := speech(60 * time.Second)
	r := newRig(t, rigOptions{script: fake.Script{Replies: []fake.Reply{{Result: engine.Result{Text: "slow"}, Delay: 20 * time.Millisecond}}}})
	started := make(chan struct{})
	var once sync.Once
	result := make(chan error, 1)
	go func() {
		_, err := r.c.Transcribe(context.Background(), samples, rate, TranscribeOptions{Progress: func(time.Duration, time.Duration) {
			once.Do(func() { close(started) })
		}})
		result <- err
	}()
	<-started
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)
	if got := r.out.delivered(); len(got) != 1 {
		t.Errorf("the dictation delivered %q", got)
	}
	if err := <-result; err != nil {
		t.Fatalf("the transcription around the dictation: %v", err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A file's transcript goes through the cleanup and LLM stages, as a
// dictation's does; an LLM stage that fails passes the cleaned text on.
func TestTranscribeRunsTheTextStages(t *testing.T) {
	llmErr := errors.New("no answer")
	for _, tc := range []struct {
		name string
		llm  TextStage
		want string
	}{
		{"no LLM stage", nil, "cleaned"},
		{"LLM", TextStageFunc(func(_ context.Context, text string, info SessionInfo) (string, error) {
			if info.Settings.HistoryLimit != 7 {
				t.Errorf("the LLM stage got settings %+v, want the current ones", info.Settings)
			}
			return text + " by the model", nil
		}), "cleaned by the model"},
		{"failing LLM", TextStageFunc(func(context.Context, string, SessionInfo) (string, error) { return "", llmErr }), "cleaned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, rigOptions{
				script:   fake.Script{Replies: []fake.Reply{fake.Transcript("raw words")}},
				settings: func(s *config.Settings) { s.HistoryLimit = 7 },
				stages: Stages{
					Cleanup: TextStageFunc(func(context.Context, string, SessionInfo) (string, error) { return "cleaned", nil }),
					LLM:     tc.llm,
				},
			})
			got, err := r.c.Transcribe(context.Background(), speech(3*time.Second), rate, TranscribeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Text != tc.want || got.RawText != "raw words" {
				t.Errorf("got %+v, want text %q", got, tc.want)
			}
		})
	}
}
