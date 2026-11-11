//go:build cgo && nemo

package nemo

/*
#cgo pkg-config: nemo-speech-asr
#include "shim.h"
*/
import "C"

import (
	"io"
	"sync"
	"unsafe"

	"github.com/opragmatico/goscriber/internal/engine"
)

// stream is a NeMo-Speech.cpp streaming recognition: an engine.Stream.
type stream struct {
	h        *handles
	interims bool // the caller asked for interim results

	// mu is held by every call. A call that finds it held fails instead of
	// waiting: two goroutines are driving the stream at once, which the
	// library does not allow. Close waits for it.
	mu       sync.Mutex
	c        *C.nemo_speech_asr_stream // nil once closed
	finished bool                      // Finish succeeded
	done     bool                      // Next returned the end-of-stream final
}

var _ engine.Stream = (*stream)(nil)

// begin locks the stream for one call and returns the function that unlocks
// it. It holds the recognizer's lock for reading too, so Unload waits for the
// call.
func (s *stream) begin(op string) (func(), error) {
	s.h.mu.RLock()
	if !s.mu.TryLock() {
		s.h.mu.RUnlock()
		return nil, invalid(op, "the stream is in use by another goroutine; drive a stream from one goroutine at a time")
	}
	if s.c == nil {
		s.mu.Unlock()
		s.h.mu.RUnlock()
		return nil, invalid(op, "the stream is closed")
	}
	return func() {
		s.mu.Unlock()
		s.h.mu.RUnlock()
	}, nil
}

// Push implements engine.Stream. The library copies the samples into its own
// buffer before it returns.
func (s *stream) Push(samples []float32, sampleRate int) error {
	const op = "stream push"
	end, err := s.begin(op)
	if err != nil {
		return err
	}
	defer end()

	if s.finished {
		return invalid(op, "the stream is finished")
	}
	if len(samples) == 0 {
		return nil
	}
	if err := checkRate(op, sampleRate); err != nil {
		return err
	}
	var msg *C.char
	status := C.gs_stream_push_f32(s.c,
		(*C.float)(unsafe.Pointer(&samples[0])), C.size_t(len(samples)), C.int32_t(sampleRate), &msg)
	if status != C.NEMO_SPEECH_ASR_OK {
		return statusError(op, status, msg)
	}
	return nil
}

// Next implements engine.Stream. NeMo-Speech.cpp v0.3.0 returns interim
// results whatever the request's interim_results says, so Next drops them
// itself when the stream was started without engine.Options.InterimResults.
func (s *stream) Next() (*engine.Result, error) {
	const op = "stream next"
	end, err := s.begin(op)
	if err != nil {
		return nil, err
	}
	defer end()

	for !s.done {
		var res *C.nemo_speech_asr_result
		var msg *C.char
		if status := C.gs_stream_next(s.c, &res, &msg); status != C.NEMO_SPEECH_ASR_OK {
			return nil, statusError(op, status, msg)
		}
		if res == nil {
			if s.finished {
				break
			}
			return nil, nil
		}
		r := copyResult(res)
		C.nemo_speech_asr_result_destroy(res)
		if s.finished {
			// After Finish the library returns the end-of-stream final,
			// then nothing.
			r.Final = true
			s.done = true
		}
		if r.Final || s.interims {
			return &r, nil
		}
	}
	s.done = true
	return nil, io.EOF
}

// ForceEndpoint implements engine.Stream.
func (s *stream) ForceEndpoint() error {
	const op = "stream force endpoint"
	end, err := s.begin(op)
	if err != nil {
		return err
	}
	defer end()

	if s.finished {
		return invalid(op, "the stream is finished")
	}
	var msg *C.char
	if status := C.gs_stream_force_endpoint(s.c, &msg); status != C.NEMO_SPEECH_ASR_OK {
		return statusError(op, status, msg)
	}
	return nil
}

// Finish implements engine.Stream. The library decodes the audio it still
// holds as lookahead here, so Finish takes about as long as one chunk of
// decoding; Next then returns the end-of-stream final at once.
func (s *stream) Finish() error {
	const op = "stream finish"
	end, err := s.begin(op)
	if err != nil {
		return err
	}
	defer end()

	if s.finished {
		return invalid(op, "the stream is already finished")
	}
	var msg *C.char
	if status := C.gs_stream_finish(s.c, &msg); status != C.NEMO_SPEECH_ASR_OK {
		return statusError(op, status, msg)
	}
	s.finished = true
	return nil
}

// Close implements engine.Stream. It waits for a call in flight on another
// goroutine.
func (s *stream) Close() error {
	s.h.mu.RLock()
	defer s.h.mu.RUnlock()
	s.mu.Lock()
	s.closeLocked()
	s.mu.Unlock()

	s.h.streamsMu.Lock()
	delete(s.h.streams, s)
	s.h.streamsMu.Unlock()
	return nil
}

// closeLocked releases the native stream. s.mu must be held, and the
// recognizer must still exist.
func (s *stream) closeLocked() {
	if s.c != nil {
		C.nemo_speech_asr_stream_close(s.c)
		s.c = nil
	}
}
