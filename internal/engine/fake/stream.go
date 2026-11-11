package fake

import (
	"io"
	"sync"
	"time"

	"github.com/opragmatico/goscriber/internal/engine"
)

// StreamScript scripts one stream.
type StreamScript struct {
	// Results come out of Next in order as audio arrives: each one once the
	// stream has received at least its AudioProcessed of audio. Interim
	// results are skipped unless the stream asked for them; finals among
	// them model endpointing. Results not reached by Finish are dropped, as
	// the real engine only reports the end-of-stream final after it.
	Results []engine.Result
	// Final is the end-of-stream final Next returns after Finish. It is
	// marked final and, when AudioProcessed is 0, covers all the audio.
	Final engine.Result
	// StartErr makes StartStream fail.
	StartErr error
	// PushErr, NextErr and FinishErr make every call to that method fail.
	PushErr, NextErr, FinishErr error
	// FinishDelay is how long Finish takes, like the real engine flushing the
	// audio it holds back as lookahead.
	FinishDelay time.Duration
}

// Stream is a fake engine.Stream, following its StreamScript.
type Stream struct {
	eng    *Engine
	script StreamScript
	opts   engine.Options

	mu        sync.Mutex
	rate      int // fixed by the first Push with samples; 0 until then
	samples   int // pushed so far
	next      int // index of the next scripted result
	text      string
	endpoint  bool
	finished  bool
	finalSent bool
	closed    bool
}

var _ engine.Stream = (*Stream)(nil)

// Push implements engine.Stream.
func (s *Stream) Push(samples []float32, sampleRate int) error {
	const op = "stream push"
	s.eng.life.RLock()
	defer s.eng.life.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.usableLocked(op); err != nil {
		return err
	}
	if s.finished {
		return invalid(op, "the stream is finished")
	}
	if len(samples) == 0 {
		return nil
	}
	rate, err := checkRate(op, sampleRate)
	if err != nil {
		return err
	}
	if s.rate != 0 && rate != s.rate {
		return invalid(op, "the sample rate cannot change within a stream")
	}
	if s.script.PushErr != nil {
		return wrap(op, s.script.PushErr)
	}
	s.rate = rate
	s.samples += len(samples)
	return nil
}

// Next implements engine.Stream.
func (s *Stream) Next() (*engine.Result, error) {
	const op = "stream next"
	s.eng.life.RLock()
	defer s.eng.life.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.usableLocked(op); err != nil {
		return nil, err
	}
	if s.script.NextErr != nil {
		return nil, wrap(op, s.script.NextErr)
	}
	if s.finished {
		if s.finalSent {
			return nil, io.EOF
		}
		s.finalSent = true
		r := cloneResult(s.script.Final)
		r.Final = true
		if r.AudioProcessed == 0 {
			r.AudioProcessed = s.audioLocked()
		}
		return &r, nil
	}
	if s.endpoint {
		s.endpoint = false
		r := engine.Result{Text: s.text, Final: true, AudioProcessed: s.audioLocked()}
		s.text = ""
		return &r, nil
	}
	for s.next < len(s.script.Results) && s.script.Results[s.next].AudioProcessed <= s.audioLocked() {
		r := cloneResult(s.script.Results[s.next])
		s.next++
		if r.Final {
			s.text = ""
			return &r, nil
		}
		if s.opts.InterimResults {
			s.text = r.Text
			return &r, nil
		}
	}
	return nil, nil
}

// ForceEndpoint implements engine.Stream: the next result is a final holding
// the latest interim text.
func (s *Stream) ForceEndpoint() error {
	const op = "stream force endpoint"
	s.eng.life.RLock()
	defer s.eng.life.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.usableLocked(op); err != nil {
		return err
	}
	if s.finished {
		return invalid(op, "the stream is finished")
	}
	s.endpoint = true
	return nil
}

// Finish implements engine.Stream.
func (s *Stream) Finish() error {
	const op = "stream finish"
	s.eng.life.RLock()
	defer s.eng.life.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.usableLocked(op); err != nil {
		return err
	}
	if s.finished {
		return invalid(op, "the stream is already finished")
	}
	if s.script.FinishErr != nil {
		return wrap(op, s.script.FinishErr)
	}
	if s.script.FinishDelay > 0 {
		time.Sleep(s.script.FinishDelay)
	}
	s.finished = true
	return nil
}

// Close implements engine.Stream.
func (s *Stream) Close() error {
	s.markClosed()
	s.eng.forget(s)
	return nil
}

func (s *Stream) markClosed() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

func (s *Stream) usableLocked(op string) error {
	if s.closed {
		return invalid(op, "the stream is closed")
	}
	return nil
}

func (s *Stream) audioLocked() time.Duration {
	if s.rate == 0 {
		return 0
	}
	return duration(s.samples, s.rate)
}

// Options returns the options the stream was started with.
func (s *Stream) Options() engine.Options {
	return cloneOptions(s.opts)
}

// Audio returns how much audio has been pushed.
func (s *Stream) Audio() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.audioLocked()
}

// SampleRate returns the stream's sample rate, or 0 before the first Push.
func (s *Stream) SampleRate() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rate
}

// Finished reports whether Finish succeeded.
func (s *Stream) Finished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished
}

// Closed reports whether the stream was closed, by Close or by Unload.
func (s *Stream) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
