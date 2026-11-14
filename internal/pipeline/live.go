package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/engine"
)

// liveDecoder decodes a recording while it goes on, so the overlay can show
// the words and only the end of the recording is left to decode when it
// stops. Its methods run on the engine goroutine.
type liveDecoder interface {
	// push decodes the next samples of the recording as far as it can. It
	// reports the transcript so far when that changed.
	push(samples []float32) (sofar interim, changed bool)
	// finish returns the final transcript of the whole recording, all of
	// whose samples it is given. If decoding live failed along the way, it
	// decodes what was missed from samples instead.
	finish(ctx context.Context, samples []float32) (engine.Result, error)
	// close releases the decoder. It is safe to call more than once.
	close()
}

// interim is the transcript so far: Text, whose first Stable bytes are
// settled and the rest may still change.
type interim struct {
	text   string
	stable int
}

// liveOptions are what a live decoder needs to know about the session.
type liveOptions struct {
	engine engine.Engine
	ctx    context.Context
	opts   engine.Options // the session's recognition options
	rate   int            // of the recording
	gate   SpeechGate     // chunks it finds silent are not decoded
	log    *slog.Logger
}

// streamDecoder decodes a recording through an engine stream: audio is
// pushed as it arrives and the stream's interim results are the transcript
// so far. If the stream fails, finish decodes the recording whole instead.
type streamDecoder struct {
	liveOptions
	stream engine.Stream // nil once failed or closed
	err    error         // why the stream failed

	finals []engine.Result // endpointed utterances, in order
	text   string          // the current utterance's latest interim text, trimmed
	stable int             // bytes of text whose words the previous interim had too
	shown  interim         // the transcript so far, as push last reported it
}

// startStreamDecoder opens a stream for the recording. A stream that cannot
// start leaves the decoder failed, to decode the recording whole at finish.
func startStreamDecoder(o liveOptions) *streamDecoder {
	d := &streamDecoder{liveOptions: o}
	opts := o.opts
	opts.InterimResults = true
	s, err := o.engine.StartStream(o.ctx, opts)
	if err != nil {
		d.fail(err)
		return d
	}
	d.stream = s
	return d
}

func (d *streamDecoder) push(samples []float32) (interim, bool) {
	if d.stream == nil {
		return interim{}, false
	}
	if err := d.stream.Push(samples, d.rate); err != nil {
		d.fail(err)
		return interim{}, false
	}
	changed := false
	for {
		r, err := d.stream.Next()
		if err != nil {
			d.fail(err)
			return interim{}, false
		}
		if r == nil {
			break
		}
		if r.Final {
			d.finals = append(d.finals, *r)
			d.text, d.stable = "", 0
		} else {
			text := strings.TrimSpace(r.Text)
			d.stable = commonWordPrefix(d.text, text)
			d.text = text
		}
		changed = true
	}
	// The stream repeats its hypothesis while nothing new is heard, such as
	// in a pause: only a change is news.
	if !changed {
		return interim{}, false
	}
	if sofar := d.sofar(); sofar != d.shown {
		d.shown = sofar
		return sofar, true
	}
	return interim{}, false
}

// sofar joins the finals and the latest interim. The finals are stable, and
// so are the words the latest interim shares with the one before it.
func (d *streamDecoder) sofar() interim {
	var b strings.Builder
	for _, f := range d.finals {
		appendWords(&b, f.Text)
	}
	stable := b.Len()
	if d.text != "" {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		if d.stable > 0 {
			stable = b.Len() + d.stable
		}
		b.WriteString(d.text)
	}
	return interim{text: b.String(), stable: stable}
}

func (d *streamDecoder) finish(ctx context.Context, samples []float32) (engine.Result, error) {
	if d.stream != nil {
		if err := d.stream.Finish(); err != nil {
			d.fail(err)
		}
	}
	for d.stream != nil {
		r, err := d.stream.Next()
		if errors.Is(err, io.EOF) || (err == nil && r == nil) {
			break
		}
		if err != nil {
			d.fail(err)
			break
		}
		if r.Final {
			d.finals = append(d.finals, *r)
		}
	}
	if d.stream != nil {
		d.close()
		return engine.JoinFinals(d.finals), nil
	}
	if err := ctx.Err(); err != nil {
		return engine.Result{}, err
	}
	d.log.Warn("live transcription failed; transcribing the whole recording instead", "err", d.err)
	return d.engine.Recognize(ctx, samples, d.rate, d.opts)
}

// fail gives up on the stream.
func (d *streamDecoder) fail(err error) {
	if d.ctx.Err() == nil && d.err == nil {
		d.err = err
		d.log.Debug("the live transcription stream failed", "err", err)
	}
	d.close()
}

func (d *streamDecoder) close() {
	if d.stream != nil {
		_ = d.stream.Close()
		d.stream = nil
	}
}

// chunkDecoder decodes a recording for an offline model in chunks cut at
// pauses: each chunk is decoded with Recognize as soon as it is complete,
// while the recording goes on, so only the open chunk is left to decode when
// it stops. Silent chunks are skipped. If a chunk fails, finish decodes
// everything from that chunk on in one piece instead.
type chunkDecoder struct {
	liveOptions
	chunker *chunker
	results []engine.Result // of the chunks decoded, placed in the recording
	failed  bool            // a chunk failed: nothing more is decoded live
	from    int             // where in the recording the undecoded audio starts
	chunks  int             // chunks decoded, for the log
}

func newChunkDecoder(o liveOptions, plan chunkPlan) *chunkDecoder {
	return &chunkDecoder{liveOptions: o, chunker: newChunker(o.rate, plan)}
}

func (d *chunkDecoder) push(samples []float32) (interim, bool) {
	if d.failed {
		return interim{}, false
	}
	changed := false
	for _, c := range d.chunker.add(samples) {
		if d.failed {
			break
		}
		r, ok, err := d.decode(d.ctx, c)
		if err != nil {
			if d.ctx.Err() == nil {
				d.log.Debug("a chunk failed to decode; the rest of the recording will be decoded in one piece", "err", err)
			}
			d.failed = true
			break
		}
		d.from = c.start + len(c.samples)
		if ok {
			d.results = append(d.results, r)
			changed = changed || strings.TrimSpace(r.Text) != ""
		}
	}
	if !changed {
		return interim{}, false
	}
	text := engine.JoinFinals(d.results).Text
	return interim{text: text, stable: len(text)}, true
}

// decode decodes one chunk, unless it is silent, and places its result in
// the recording.
func (d *chunkDecoder) decode(ctx context.Context, c chunk) (engine.Result, bool, error) {
	if len(c.samples) == 0 {
		return engine.Result{}, false, nil
	}
	if t := d.gate.SilenceThreshold; t > 0 && audio.Loudest(c.samples, d.rate).RMS < t {
		return engine.Result{}, false, nil
	}
	r, err := d.engine.Recognize(ctx, c.samples, d.rate, d.opts)
	if err != nil {
		return engine.Result{}, false, err
	}
	d.chunks++
	return r.Offset(d.duration(c.start)), true, nil
}

func (d *chunkDecoder) finish(ctx context.Context, samples []float32) (engine.Result, error) {
	from := min(d.from, len(samples))
	if d.failed {
		d.log.Warn("live transcription failed; transcribing the rest of the recording in one piece",
			"from", d.duration(from).Round(time.Millisecond))
	}
	// What is left: the open chunk, or everything since the chunk that
	// failed. The chunks pushed add up to samples, so either way it is the
	// end of samples.
	r, ok, err := d.decode(ctx, chunk{start: from, samples: samples[from:]})
	if err != nil {
		return engine.Result{}, err
	}
	results := d.results
	if ok {
		results = append(results, r)
	}
	if d.chunks > 1 {
		d.log.Debug("decoded the recording in chunks", "chunks", d.chunks, "audio", d.duration(len(samples)).Round(time.Millisecond))
	}
	if len(results) == 0 {
		// Every chunk was silent.
		return engine.Result{Final: true, AudioProcessed: d.duration(len(samples))}, nil
	}
	return engine.JoinFinals(results), nil
}

func (d *chunkDecoder) close() {}

func (d *chunkDecoder) duration(samples int) time.Duration {
	if d.rate <= 0 {
		return 0
	}
	return time.Duration(samples) * time.Second / time.Duration(d.rate)
}

// commonWordPrefix returns how many bytes at the start of cur hold the words
// prev starts with too: the part of a new hypothesis that did not change.
func commonWordPrefix(prev, cur string) int {
	end := 0
	p, c := strings.Fields(prev), strings.Fields(cur)
	rest := cur
	for i := 0; i < len(p) && i < len(c) && p[i] == c[i]; i++ {
		at := strings.Index(rest, c[i])
		rest = rest[at+len(c[i]):]
		end = len(cur) - len(rest)
	}
	return end
}

// appendWords appends text to b, trimmed, with a space before it unless b is
// empty.
func appendWords(b *strings.Builder, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteByte(' ')
	}
	b.WriteString(text)
}
