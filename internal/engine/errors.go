package engine

import (
	"errors"
	"strings"
)

// Kinds of engine failure. Engines return them inside an *Error; test for
// them with errors.Is. All but ErrNotLoaded match a NeMo-Speech.cpp status
// code (nemo_speech_asr_status).
var (
	// ErrInvalidArgument: the request cannot be served as asked, such as
	// empty audio, an unsupported sample rate, or a stream on a model that
	// cannot stream (NEMO_SPEECH_ASR_ERROR_INVALID_ARGUMENT).
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrOutOfMemory: the engine ran out of memory
	// (NEMO_SPEECH_ASR_ERROR_OUT_OF_MEMORY).
	ErrOutOfMemory = errors.New("out of memory")
	// ErrRuntime: the engine failed, for example on a corrupt model file
	// or a backend error (NEMO_SPEECH_ASR_ERROR_RUNTIME).
	ErrRuntime = errors.New("runtime error")
	// ErrCancelled: the request was cancelled (NEMO_SPEECH_ASR_ERROR_CANCELLED).
	// An *Error for a cancelled context also wraps the context's error.
	ErrCancelled = errors.New("cancelled")
	// ErrResourceExhausted: the engine is at capacity right now; retrying
	// later may work (NEMO_SPEECH_ASR_ERROR_RESOURCE_EXHAUSTED).
	ErrResourceExhausted = errors.New("resource exhausted")
	// ErrUnavailable: the engine cannot take requests, for example while
	// shutting down (NEMO_SPEECH_ASR_ERROR_UNAVAILABLE).
	ErrUnavailable = errors.New("unavailable")
	// ErrNotLoaded: the call needs a model and none is loaded.
	ErrNotLoaded = errors.New("no model loaded")
)

// Error is an engine failure.
type Error struct {
	// Op is the operation that failed, such as "load" or "stream push".
	Op string
	// Kind is one of the Err* values above.
	Kind error
	// Detail is the engine's own explanation, if any.
	Detail string
	// Err is the underlying cause, such as context.Canceled, if any.
	Err error
}

// Error formats the failure as "engine: <op>: <kind>: <detail>: <cause>",
// leaving out the parts that are empty.
func (e *Error) Error() string {
	parts := []string{"engine"}
	for _, s := range []string{e.Op, errString(e.Kind), e.Detail, errString(e.Err)} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ": ")
}

// Unwrap returns Kind and Err, so errors.Is matches either.
func (e *Error) Unwrap() []error {
	var errs []error
	for _, err := range []error{e.Kind, e.Err} {
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
