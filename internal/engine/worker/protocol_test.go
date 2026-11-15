package worker

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/engine"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	samples := []float32{0, 0.5, -1, 1, float32(math.SmallestNonzeroFloat32)}
	opts := engine.Options{Language: "es-ES", Phrases: []string{"Kowalczyk"}, Boost: 2.5}
	in := request{ID: 7, Op: opRecognize, Rate: 48000, Options: &opts}
	if err := writeFrame(&buf, in, samples); err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(&buf, request{ID: 8, Op: opPing}, nil); err != nil {
		t.Fatal(err)
	}

	var out request
	got, err := readFrame(&buf, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != 7 || out.Op != opRecognize || out.Rate != 48000 || out.Options == nil ||
		out.Options.Language != "es-ES" || out.Options.Boost != 2.5 || !slices.Equal(out.Options.Phrases, opts.Phrases) {
		t.Errorf("request = %+v", out)
	}
	if !slices.Equal(got, samples) {
		t.Errorf("samples = %v, want %v", got, samples)
	}
	out = request{}
	got, err = readFrame(&buf, &out)
	if err != nil || out.ID != 8 || got != nil {
		t.Errorf("second frame = %+v, %v, %v", out, got, err)
	}
	if _, err := readFrame(&buf, &out); !errors.Is(err, io.EOF) {
		t.Errorf("read at the end = %v, want io.EOF", err)
	}
}

// The audio goes on the wire as little-endian float32, whatever the host.
func TestFrameAudioIsLittleEndian(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrame(&buf, request{}, []float32{1.5}); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if got := math.Float32frombits(binary.LittleEndian.Uint32(b[len(b)-4:])); got != 1.5 {
		t.Errorf("last 4 bytes decode to %v, want 1.5", got)
	}
	if n := binary.LittleEndian.Uint32(b[len(b)-8:]); n != 4 {
		t.Errorf("audio length = %d, want 4", n)
	}
}

func TestBrokenFrames(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrame(&buf, response{ID: 1}, []float32{1, 2}); err != nil {
		t.Fatal(err)
	}
	whole := buf.Bytes()
	for n := 1; n < len(whole); n++ {
		var r response
		if _, err := readFrame(bytes.NewReader(whole[:n]), &r); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("a frame cut at %d of %d bytes: err = %v, want io.ErrUnexpectedEOF", n, len(whole), err)
		}
	}

	huge := binary.LittleEndian.AppendUint32(nil, maxSection+1)
	var r response
	if _, err := readFrame(bytes.NewReader(huge), &r); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("an oversized length: err = %v", err)
	}
	odd := binary.LittleEndian.AppendUint32(nil, 2)
	odd = append(odd, "{}"...)
	odd = binary.LittleEndian.AppendUint32(odd, 3)
	odd = append(odd, 0, 0, 0)
	if _, err := readFrame(bytes.NewReader(odd), &r); err == nil {
		t.Error("audio that is not whole samples was accepted")
	}
}

func TestWriteFrameRefusesWhatCannotBeSent(t *testing.T) {
	res := engine.Result{Confidence: float32(math.NaN())}
	err := writeFrame(io.Discard, response{Result: &res}, nil)
	if !errors.Is(err, errFrame) {
		t.Errorf("a NaN: err = %v, want errFrame", err)
	}
	finite(&res)
	if err := writeFrame(io.Discard, response{Result: &res}, nil); err != nil {
		t.Errorf("after finite: %v", err)
	}
}

func TestFinite(t *testing.T) {
	inf := float32(math.Inf(1))
	r := engine.Result{Confidence: inf, Words: []engine.Word{{Confidence: float32(math.NaN())}, {Confidence: 0.5}}}
	finite(&r)
	if r.Confidence != 0 || r.Words[0].Confidence != 0 || r.Words[1].Confidence != 0.5 {
		t.Errorf("finite = %+v", r)
	}
}

func TestErrorsCrossTheWire(t *testing.T) {
	for name, kind := range kinds {
		in := &engine.Error{Op: "recognize", Kind: kind, Detail: "detail " + name}
		out := toWire(opRecognize, in).err()
		eerr, ok := errors.AsType[*engine.Error](out)
		if !ok || !errors.Is(out, kind) || eerr.Op != "recognize" || eerr.Detail != in.Detail {
			t.Errorf("%s: %v came back as %v", name, in, out)
		}
	}

	out := toWire(opLoad, &engine.Error{Op: "load", Kind: engine.ErrCancelled, Err: context.Canceled}).err()
	if !errors.Is(out, engine.ErrCancelled) || !strings.Contains(out.Error(), "context canceled") {
		t.Errorf("a cause: %v", out)
	}
	out = toWire(opLoad, errors.New("plain")).err()
	if eerr, ok := errors.AsType[*engine.Error](out); !ok || !errors.Is(out, engine.ErrRuntime) || eerr.Op != "load" || eerr.Detail != "plain" {
		t.Errorf("a plain error: %v", out)
	}
	if toWire(opLoad, nil) != nil || (*wireError)(nil).err() != nil {
		t.Error("no error must stay no error")
	}
}

func TestServeAnswersEachRequest(t *testing.T) {
	// An engine that refuses everything is enough to check the plumbing;
	// the supervisor tests drive a real worker.
	var in, out bytes.Buffer
	for i, r := range []request{
		{Op: opHello, Version: protocolVersion},
		{Op: opRecognize, Rate: 16000},
		{Op: opStreamNext, Stream: 9},
		{Op: opStreamClose, Stream: 9},
		{Op: "bogus"},
	} {
		r.ID = uint64(i + 1)
		if err := writeFrame(&in, r, []float32{0.1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Serve(&in, &out, notLoaded{}, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	got := map[uint64]response{}
	for {
		var r response
		if _, err := readFrame(&out, &r); err != nil {
			break
		}
		got[r.ID] = r
	}
	if r := got[1]; r.Version != protocolVersion || r.PID == 0 || r.Err != nil {
		t.Errorf("hello = %+v", r)
	}
	if err := got[2].Err.err(); !errors.Is(err, engine.ErrNotLoaded) {
		t.Errorf("recognize = %v, want ErrNotLoaded", err)
	}
	if err := got[3].Err.err(); !errors.Is(err, engine.ErrInvalidArgument) {
		t.Errorf("next on an unknown stream = %v", err)
	}
	if r := got[4]; r.Err != nil {
		t.Errorf("closing an unknown stream = %v", r.Err.err())
	}
	if err := got[5].Err.err(); !errors.Is(err, engine.ErrInvalidArgument) {
		t.Errorf("an unknown request = %v", err)
	}
}

type notLoaded struct{}

func (notLoaded) Load(context.Context, engine.LoadSpec) (engine.ModelInfo, error) {
	return engine.ModelInfo{}, &engine.Error{Op: "load", Kind: engine.ErrNotLoaded}
}
func (notLoaded) Unload() error { return nil }
func (notLoaded) Recognize(context.Context, []float32, int, engine.Options) (engine.Result, error) {
	return engine.Result{}, &engine.Error{Op: "recognize", Kind: engine.ErrNotLoaded}
}

func (notLoaded) StartStream(context.Context, engine.Options) (engine.Stream, error) {
	return nil, &engine.Error{Op: "start stream", Kind: engine.ErrNotLoaded}
}

func TestRelog(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: noTime}))
	relog(log, `{"time":"2026-11-15T10:00:00Z","level":"WARN","msg":"unloading the model failed","err":"boom"}`)
	relog(log, `[asr] model=nemotron backend=MTL0 vad=on`)
	relog(log, `ggml_metal_init: error: failed to allocate buffer`)
	relog(log, `{not json`)
	want := []string{
		`level=WARN msg="unloading the model failed" err=boom`,
		`level=DEBUG msg="engine output" line="[asr] model=nemotron backend=MTL0 vad=on"`,
		`level=WARN msg="engine output" line="ggml_metal_init: error: failed to allocate buffer"`,
		`level=DEBUG msg="engine output" line="{not json"`,
	}
	if got := strings.Split(strings.TrimSpace(buf.String()), "\n"); !slices.Equal(got, want) {
		t.Errorf("relogged:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func noTime(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

func TestTailKeepsTheLastLines(t *testing.T) {
	tl := &tail{max: 3}
	for _, l := range []string{"a", "b", "c", "d"} {
		tl.add(l)
	}
	if got := tl.get(); !slices.Equal(got, []string{"b", "c", "d"}) {
		t.Errorf("tail = %q", got)
	}
}

func TestTimeouts(t *testing.T) {
	s := New(Options{})
	gpu, cpu := &proc{}, &proc{cpu: true}
	if got := s.decodeTimeout(gpu, s.t.Recognize, 2*time.Second); got != time.Minute {
		t.Errorf("a short clip on the GPU: %v, want the 1 min floor", got)
	}
	if got := s.decodeTimeout(gpu, s.t.Recognize, 10*time.Minute); got != 100*time.Minute {
		t.Errorf("a long clip on the GPU: %v, want 10 times its length", got)
	}
	if got := s.decodeTimeout(cpu, s.t.Recognize, 2*time.Second); got != 4*time.Minute {
		t.Errorf("a short clip on the CPU: %v, want 4 times the floor", got)
	}
	if got := audioLength(32000, 0); got != 2*time.Second {
		t.Errorf("audioLength at the model's rate = %v", got)
	}
}
