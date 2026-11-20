package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/opragmatico/goscriber/internal/engine"
)

// Retranscription is audio transcribed outside a dictation: a saved
// recording again, or an audio file.
type Retranscription struct {
	// Text is the transcript after the cleanup and LLM stages, and RawText
	// the model's.
	Text, RawText string
	// Model is the catalog ID of the model that transcribed it.
	Model string
	// Language is the transcript's language as best known (see
	// SessionInfo.TextLanguage).
	Language string
}

// ErrClosed is returned once the coordinator has been closed.
var ErrClosed = errors.New("dictation has stopped")

// TranscribeOptions tune Transcribe.
type TranscribeOptions struct {
	// Progress, when set, is told how much of the audio has been decoded,
	// out of its whole length, after each slice of it. It is called on
	// Transcribe's goroutine, and last with done equal to total.
	Progress func(done, total time.Duration)
}

// transcribeSlice is how much audio Transcribe decodes in one turn on the
// engine goroutine. A dictation started meanwhile waits for at most one
// slice before its own work runs.
const transcribeSlice = 10 * time.Second

// transcribePush is how much audio a slice hands the decoder at a time.
const transcribePush = time.Second

// Retranscribe transcribes a saved recording again, for the History page;
// see Transcribe.
func (c *Coordinator) Retranscribe(ctx context.Context, samples []float32, rate int) (Retranscription, error) {
	return c.Transcribe(ctx, samples, rate, TranscribeOptions{})
}

// Transcribe transcribes audio, mono samples at rate Hz, with the model the
// current settings select, and has the cleanup and LLM stages process it
// as they do a dictation's transcript; a stage that fails passes its input
// on. It delivers and saves nothing.
//
// Audio of any length is decoded the way a dictation is decoded live: a
// streaming model through a stream, and an offline model in chunks cut at
// pauses, whatever long_form_chunking says, since an offline model decoding
// minutes of audio at once can lose whole stretches of it without an error
// (docs/models/parakeet-tdt-0.6b-v2.md#long-audio). The decode runs on the
// engine goroutine one slice at a time, so a dictation started meanwhile
// goes first and the transcription resumes after it. Cancelling ctx stops
// it.
//
// Audio with no speech fails with ErrNoSpeech, and with no model ready with
// ErrNoModel or ErrModelCorrupt.
func (c *Coordinator) Transcribe(ctx context.Context, samples []float32, rate int, o TranscribeOptions) (Retranscription, error) {
	if rate <= 0 {
		return Retranscription{}, fmt.Errorf("pipeline: sample rate %d", rate)
	}
	settings := c.settings()
	model, err := c.models.Resolve(settings)
	if err != nil {
		return Retranscription{}, err
	}
	phrases := boostedWords(model, settings)
	opts := engine.Options{Language: model.Language}
	if len(phrases) > 0 {
		opts.Phrases, opts.Boost = phrases, float32(settings.CustomWordBoost)
	}
	d := &audioDecode{
		c: c, ctx: ctx, model: model, samples: samples,
		live: liveOptions{
			engine: c.eng, ctx: ctx, opts: opts, rate: rate,
			gate: GateFromSettings(settings), log: c.log,
		},
	}
	// The decoder lives on the engine goroutine; so does its end, after
	// whatever slice is still queued there.
	defer c.jobs.post(d.close)

	start := time.Now()
	total := d.duration(len(samples))
	slice := max(1, d.samplesIn(transcribeSlice))
	for from := 0; ; from += slice {
		to := min(from+slice, len(samples))
		if err := c.onEngine(ctx, func() error { return d.step(from, to, to == len(samples)) }); err != nil {
			return Retranscription{}, err
		}
		if o.Progress != nil {
			o.Progress(d.duration(to), total)
		}
		if to == len(samples) {
			break
		}
	}
	res := d.result

	info := SessionInfo{
		Model:        model.Spec.ID,
		Language:     model.Language,
		Languages:    slices.Clone(res.Languages),
		TextLanguage: textLanguage(model, res.Languages),
		Boosted:      len(phrases) > 0,
		Duration:     total,
		Settings:     settings.Clone(),
	}
	text := res.Text
	for _, st := range []struct {
		name  string
		stage TextStage
	}{
		{"cleanup", c.stages.Cleanup},
		{"llm", c.stages.LLM},
	} {
		if st.stage == nil {
			continue
		}
		out, err := st.stage.Process(ctx, text, info)
		if err := ctx.Err(); err != nil {
			return Retranscription{}, err
		}
		if err != nil {
			c.log.Warn("a text stage failed on a transcription; passing its input on", "stage", st.name, "err", err)
		} else {
			text = out
		}
	}
	c.log.Info("audio transcribed", "model", model.Spec.ID, "audio", total.Round(time.Millisecond),
		"took", time.Since(start).Round(time.Millisecond), "chars", len([]rune(text)))
	return Retranscription{Text: text, RawText: res.Text, Model: model.Spec.ID, Language: info.TextLanguage}, nil
}

// onEngine runs f on the engine goroutine and returns its error, or ctx's
// once it is done. A cancelled f may still run later; it must check ctx.
func (c *Coordinator) onEngine(ctx context.Context, f func() error) error {
	done := make(chan error, 1)
	if !c.jobs.post(func() { done <- f() }) {
		return ErrClosed
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// audioDecode is one Transcribe's decode. Its methods run on the engine
// goroutine, one at a time.
type audioDecode struct {
	c       *Coordinator
	ctx     context.Context
	model   Model
	samples []float32
	live    liveOptions

	dec     liveDecoder // nil until the first slice
	release func()      // ends the hold that keeps the model loaded
	result  engine.Result
}

// step decodes samples[from:to], and finishes the decode after the last
// slice. It loads the model first if the engine does not hold it, which it
// may not after a dictation with another model ran in between.
func (d *audioDecode) step(from, to int, last bool) error {
	if err := d.ctx.Err(); err != nil {
		return err
	}
	if _, err := d.c.ensureLoaded(d.ctx, 0, d.model); err != nil {
		return &loadError{err: err}
	}
	if d.release == nil {
		if a, ok := d.c.eng.(Acquirer); ok {
			release, err := a.Acquire(d.ctx)
			if err != nil {
				return &loadError{err: err}
			}
			d.release = release
		}
	}
	if d.dec == nil {
		if d.c.info.Streaming {
			d.dec = startStreamDecoder(d.live)
		} else {
			d.dec = newChunkDecoder(d.live, defaultChunkPlan)
		}
	}
	push := max(1, d.samplesIn(transcribePush))
	for off := from; off < to; off += push {
		if err := d.ctx.Err(); err != nil {
			return err
		}
		d.dec.push(d.samples[off:min(off+push, to)])
	}
	if !last {
		return nil
	}
	res, err := d.dec.finish(d.ctx, d.samples)
	if err == nil {
		err = CheckTranscript(res)
	}
	d.result = res
	return err
}

// close releases the decoder and the hold on the model.
func (d *audioDecode) close() {
	if d.dec != nil {
		d.dec.close()
		d.dec = nil
	}
	if d.release != nil {
		d.release()
		d.release = nil
	}
}

func (d *audioDecode) duration(samples int) time.Duration {
	return time.Duration(samples) * time.Second / time.Duration(d.live.rate)
}

func (d *audioDecode) samplesIn(t time.Duration) int {
	return int(int64(d.live.rate) * int64(t) / int64(time.Second))
}
