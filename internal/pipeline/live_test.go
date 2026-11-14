package pipeline

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/fake"
)

const dictated = "ask not what your country can do"

// streaming scripts a streaming model whose streams hear dictated, a word
// every 100 ms, and whose batch decodes answer "from the whole recording".
func streaming(streams ...fake.StreamScript) fake.Script {
	if len(streams) == 0 {
		streams = []fake.StreamScript{fake.Dictation(dictated, 100*time.Millisecond)}
	}
	return fake.Script{
		Model:   engine.ModelInfo{Streaming: true},
		Streams: streams,
		Replies: []fake.Reply{fake.Transcript("from the whole recording")},
	}
}

func TestStreamingShowsInterimsAndDeliversTheFinal(t *testing.T) {
	r := newRig(t, rigOptions{script: streaming()})

	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	// Every word arrives while recording.
	last := r.events.waitFor(t, "the whole interim", func(ev Event) bool { return ev.Kind == EventInterim && ev.Text == dictated })
	if want := len("ask not what your country can"); last.Stable != want || last.State != StateRecording || last.Session != 1 {
		t.Errorf("last interim %+v, want the words before the last one stable (%d bytes)", last, want)
	}
	prev := ""
	for _, ev := range r.events.kinds(EventInterim) {
		if !strings.HasPrefix(ev.Text, prev) || ev.Stable > len(ev.Text) {
			t.Errorf("interim %q (stable %d) after %q", ev.Text, ev.Stable, prev)
		}
		prev = ev.Text
	}

	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)
	if got := r.out.delivered(); !slices.Equal(got, []string{dictated}) {
		t.Errorf("delivered %q, want the stream's final", got)
	}
	if n := len(r.eng.Recognitions()); n != 0 {
		t.Errorf("%d batch decodes; the stream's final should be the transcript", n)
	}
	streams := r.eng.Streams()
	if len(streams) != 1 {
		t.Fatalf("%d streams, want 1", len(streams))
	}
	s := streams[0]
	if o := s.Options(); !o.InterimResults || o.Language != "en-US" {
		t.Errorf("stream options %+v", o)
	}
	if !s.Finished() || !s.Closed() || s.SampleRate() != rate || s.Audio() != time.Second {
		t.Errorf("stream finished %v, closed %v, %d Hz, %v of audio; want all of the recording, finished and closed",
			s.Finished(), s.Closed(), s.SampleRate(), s.Audio())
	}
	for _, ev := range r.events.kinds(EventInterim) {
		if ev.State != StateRecording {
			t.Errorf("interim %+v outside recording", ev)
		}
	}
}

func TestStreamingFallsBackToTheWholeRecording(t *testing.T) {
	boom := errors.New("backend fault")
	for name, script := range map[string]fake.StreamScript{
		"start":  {StartErr: engine.ErrRuntime},
		"push":   {PushErr: boom},
		"next":   {NextErr: boom},
		"finish": {FinishErr: boom},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, rigOptions{script: streaming(script)})
			r.c.Start()
			r.events.waitState(t, StateRecording, 1)
			r.c.Stop()
			r.events.waitState(t, StateIdle, 1)

			if got := r.out.delivered(); !slices.Equal(got, []string{"from the whole recording"}) {
				t.Errorf("delivered %q, want the batch decode", got)
			}
			recs := r.eng.Recognitions()
			if len(recs) != 1 || recs[0].Samples != rate || recs[0].SampleRate != rate || recs[0].Options.InterimResults {
				t.Errorf("recognitions %+v, want one of the whole recording", recs)
			}
			for _, s := range r.eng.Streams() {
				if !s.Closed() {
					t.Error("a failed stream was left open")
				}
			}
			if codes := r.notices.codes(); len(codes) != 0 {
				t.Errorf("notices %v; the fallback worked", codes)
			}
		})
	}
}

func TestStreamingCancelAndNoSpeechCloseTheStream(t *testing.T) {
	r := newRig(t, rigOptions{script: streaming()})
	r.c.Start()
	r.events.waitFor(t, "an interim", func(ev Event) bool { return ev.Kind == EventInterim })
	r.c.Cancel()
	r.events.waitState(t, StateIdle, 1)
	waitClosed := func(n int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			streams := r.eng.Streams()
			if len(streams) == n && streams[n-1].Closed() {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("stream %d not closed", n)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitClosed(1)
	if r.eng.Streams()[0].Finished() || len(r.out.delivered()) != 0 || len(r.eng.Recognitions()) != 0 {
		t.Error("a cancelled stream was finished or transcribed")
	}

	// A recording too short to hold speech never reaches the model's
	// final, and its stream is closed too.
	r.rec.samples = tone(100*time.Millisecond, 0.1)
	r.c.Start()
	r.events.waitState(t, StateRecording, 2)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 2)
	waitClosed(2)
	if len(r.out.delivered()) != 0 {
		t.Errorf("delivered %q", r.out.delivered())
	}
}

func TestStreamingJoinsEndpointedUtterances(t *testing.T) {
	script := fake.StreamScript{
		Results: []engine.Result{
			{Text: "first", AudioProcessed: 100 * time.Millisecond},
			{Text: "first part.", Final: true, AudioProcessed: 200 * time.Millisecond},
			{Text: "second", AudioProcessed: 300 * time.Millisecond},
		},
		Final: engine.Result{Text: "second part."},
	}
	r := newRig(t, rigOptions{script: streaming(script)})
	r.c.Start()
	ev := r.events.waitFor(t, "the second utterance", func(ev Event) bool {
		return ev.Kind == EventInterim && strings.HasSuffix(ev.Text, "second")
	})
	if ev.Text != "first part. second" || ev.Stable != len("first part.") {
		t.Errorf("interim %+v, want the endpointed utterance stable", ev)
	}
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)
	if got := r.out.delivered(); !slices.Equal(got, []string{"first part. second part."}) {
		t.Errorf("delivered %q", got)
	}
}

// pausedSpeech is 4 s of speech, a 1 s pause, 4 s more, a pause, and 2 s:
// three chunks.
func pausedSpeech() []float32 {
	return concat(speech(4*time.Second), pause(time.Second), speech(4*time.Second), pause(time.Second), speech(2*time.Second))
}

func TestChunkingDecodesWhileRecordingAndJoins(t *testing.T) {
	samples := pausedSpeech()
	r := newRig(t, rigOptions{
		samples: samples,
		script:  fake.Script{Replies: []fake.Reply{fake.Transcript("One."), fake.Transcript("Two."), fake.Transcript("Three.")}},
	})
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	ev := r.events.waitFor(t, "two chunks", func(ev Event) bool { return ev.Kind == EventInterim && ev.Text == "One. Two." })
	if ev.Stable != len(ev.Text) || ev.State != StateRecording {
		t.Errorf("interim %+v, want decoded chunks all stable", ev)
	}
	if n := len(r.eng.Recognitions()); n != 2 {
		t.Errorf("%d chunks decoded while recording, want 2", n)
	}

	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)
	if got := r.out.delivered(); !slices.Equal(got, []string{"One. Two. Three."}) {
		t.Errorf("delivered %q", got)
	}
	total := 0
	for _, rc := range r.eng.Recognitions() {
		total += rc.Samples
		if rc.SampleRate != rate || rc.Options.Language != "en-US" {
			t.Errorf("recognition %+v", rc)
		}
	}
	if n := len(r.eng.Recognitions()); n != 3 || total != len(samples) {
		t.Errorf("%d chunks of %d samples in all, want 3 covering the %d recorded, none twice", n, total, len(samples))
	}
}

func TestChunkingSkipsSilentChunks(t *testing.T) {
	samples := concat(speech(4*time.Second), make([]float32, at(8*time.Second)), speech(2*time.Second))
	r := newRig(t, rigOptions{
		samples: samples,
		script:  fake.Script{Replies: []fake.Reply{fake.Transcript("Before."), fake.Transcript("After.")}},
	})
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.events.waitFor(t, "the first chunk", func(ev Event) bool { return ev.Kind == EventInterim })
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)
	if got := r.out.delivered(); !slices.Equal(got, []string{"Before. After."}) {
		t.Errorf("delivered %q", got)
	}
	if n := len(r.eng.Recognitions()); n != 2 {
		t.Errorf("%d decodes, want the silence skipped", n)
	}
}

func TestChunkingFailureDecodesTheRestInOnePiece(t *testing.T) {
	samples := pausedSpeech()
	r := newRig(t, rigOptions{
		samples: samples,
		script: fake.Script{Replies: []fake.Reply{
			fake.Transcript("One."), {Err: engine.ErrRuntime}, fake.Transcript("Two. Three."),
		}},
	})
	r.c.Start()
	r.events.waitFor(t, "the first chunk", func(ev Event) bool { return ev.Kind == EventInterim })
	deadline := time.Now().Add(3 * time.Second)
	for len(r.eng.Recognitions()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)
	if got := r.out.delivered(); !slices.Equal(got, []string{"One. Two. Three."}) {
		t.Errorf("delivered %q", got)
	}
	// The failed chunk is decoded again, with everything after it.
	recs := r.eng.Recognitions()
	if len(recs) != 3 || recs[0].Samples+recs[2].Samples != len(samples) || recs[2].Samples <= recs[1].Samples {
		t.Errorf("recognitions %+v, want the rest decoded in one piece from the failed chunk on", recs)
	}
}

func TestChunkingCanBeTurnedOff(t *testing.T) {
	samples := pausedSpeech()
	r := newRig(t, rigOptions{
		samples:  samples,
		script:   fake.Script{Replies: []fake.Reply{fake.Transcript("all at once")}},
		settings: func(s *config.Settings) { s.LongFormChunking = false },
	})
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	r.c.Stop()
	r.events.waitState(t, StateIdle, 1)
	recs := r.eng.Recognitions()
	if len(recs) != 1 || recs[0].Samples != len(samples) || len(r.events.kinds(EventInterim)) != 0 {
		t.Errorf("recognitions %+v, interims %v; want one decode of the whole recording", recs, r.events.kinds(EventInterim))
	}
	if got := r.out.delivered(); !slices.Equal(got, []string{"all at once"}) {
		t.Errorf("delivered %q", got)
	}
}

func TestChunkingCancelDropsPendingChunks(t *testing.T) {
	slow := fake.Transcript("slow")
	slow.Delay = 200 * time.Millisecond
	r := newRig(t, rigOptions{samples: pausedSpeech(), script: fake.Script{Replies: []fake.Reply{slow}}})
	r.c.Start()
	r.events.waitState(t, StateRecording, 1)
	time.Sleep(50 * time.Millisecond) // the first chunk is decoding
	r.c.Cancel()
	r.events.waitState(t, StateIdle, 1)
	time.Sleep(300 * time.Millisecond)
	if n := len(r.eng.Recognitions()); n > 1 {
		t.Errorf("%d chunks decoded after the cancel, want the pending ones dropped", n)
	}
	if len(r.out.delivered()) != 0 || len(r.events.kinds(EventInterim)) != 0 {
		t.Error("a cancelled recording was shown or delivered")
	}
}

func TestCommonWordPrefix(t *testing.T) {
	for _, tc := range []struct {
		prev, cur string
		want      int
	}{
		{"", "ask", 0},
		{"ask", "ask not", 3},
		{"ask not wh", "ask not what you", 7},
		{"ask not what", "ask not what", 12},
		{"Ask", "ask not", 0},
		{"ask  not", "ask  not  what", 8},
	} {
		if got := commonWordPrefix(tc.prev, tc.cur); got != tc.want {
			t.Errorf("commonWordPrefix(%q, %q) = %d, want %d", tc.prev, tc.cur, got, tc.want)
		}
	}
}

func TestSubscriberKeepsTheLatestInterim(t *testing.T) {
	s := &subscriber{wake: make(chan struct{}, 1)}
	s.push(Event{Kind: EventState, Session: 1, State: StateRecording})
	s.push(Event{Kind: EventInterim, Session: 1, Text: "a"})
	s.push(Event{Kind: EventLevel, Session: 1})
	s.push(Event{Kind: EventInterim, Session: 1, Text: "a b"})
	s.push(Event{Kind: EventState, Session: 1, State: StateTranscribing})
	s.push(Event{Kind: EventInterim, Session: 1, Text: "a b c"})
	var got []string
	for _, ev := range s.queue {
		got = append(got, ev.Kind.String()+":"+ev.Text)
	}
	want := []string{"state:", "interim:a b", "level:", "state:", "interim:a b c"}
	if !slices.Equal(got, want) {
		t.Errorf("queue %v, want %v", got, want)
	}
}
