//go:build cgo && nemo

package nemo

/*
#cgo pkg-config: nemo-speech-asr
#include "shim.h"
*/
import "C"

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"time"
	"unsafe"

	"github.com/opragmatico/goscriber/internal/engine"
)

// Engine is the NeMo-Speech.cpp implementation of engine.Engine. It holds at
// most one recognizer, which serves concurrent calls: the library allows a
// recognizer to be shared across threads. Transcripts keep the punctuation
// and casing the model writes. The zero value is not usable; call New.
type Engine struct {
	h *handles
}

// handles owns the native recognizer. It lives apart from Engine so that a
// cleanup can release it when an Engine is dropped without Unload. That is a
// safety net only: call Unload.
type handles struct {
	// mu is held for reading by every call into the recognizer and for
	// writing by Load and Unload, so these wait for calls in flight.
	mu   sync.RWMutex
	rec  *C.nemo_speech_asr_recognizer
	info engine.ModelInfo
}

var _ engine.Engine = (*Engine)(nil)

// New returns an Engine with no model loaded.
func New() *Engine {
	e := &Engine{h: &handles{}}
	runtime.AddCleanup(e, func(h *handles) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.releaseLocked()
	}, e.h)
	return e
}

// statusKinds maps NeMo-Speech.cpp status codes to engine errors.
var statusKinds = map[C.nemo_speech_asr_status]error{
	C.NEMO_SPEECH_ASR_ERROR_INVALID_ARGUMENT:   engine.ErrInvalidArgument,
	C.NEMO_SPEECH_ASR_ERROR_OUT_OF_MEMORY:      engine.ErrOutOfMemory,
	C.NEMO_SPEECH_ASR_ERROR_RUNTIME:            engine.ErrRuntime,
	C.NEMO_SPEECH_ASR_ERROR_CANCELLED:          engine.ErrCancelled,
	C.NEMO_SPEECH_ASR_ERROR_RESOURCE_EXHAUSTED: engine.ErrResourceExhausted,
	C.NEMO_SPEECH_ASR_ERROR_UNAVAILABLE:        engine.ErrUnavailable,
}

// statusError turns a failed call's status and the error message the shim
// copied into an *engine.Error, and frees the message.
func statusError(op string, status C.nemo_speech_asr_status, msg *C.char) error {
	detail := ""
	if msg != nil {
		detail = C.GoString(msg)
		C.free(unsafe.Pointer(msg))
	}
	kind, ok := statusKinds[status]
	if !ok {
		kind = engine.ErrRuntime
		detail = fmt.Sprintf("status %d: %s", int(status), detail)
	}
	return &engine.Error{Op: op, Kind: kind, Detail: detail}
}

// checkSDK refuses a runtime whose version is not compatible with the one
// the binding was written for: the loader may have found another copy of
// the library.
var checkSDK = sync.OnceValue(func() error { return checkVersion(Version()) })

// Load implements engine.Engine. It loads the GGUF at spec.Path on Metal,
// or on the CPU when spec.CPU is set, after checking that the file exists
// (engine.ErrModelNotFound otherwise) and reading what the model can do from
// its metadata.
func (e *Engine) Load(ctx context.Context, spec engine.LoadSpec) (engine.ModelInfo, error) {
	const op = "load"
	if err := checkSDK(); err != nil {
		return engine.ModelInfo{}, err
	}
	h := e.h
	h.mu.Lock()
	defer h.mu.Unlock()
	h.releaseLocked()

	if err := ctx.Err(); err != nil {
		return engine.ModelInfo{}, cancelled(op, err)
	}
	meta, err := inspectModel(op, spec.Path)
	if err != nil {
		return engine.ModelInfo{}, err
	}
	if spec.VADPath != "" {
		if err := checkFile(op, spec.VADPath); err != nil {
			return engine.ModelInfo{}, err
		}
	}

	// The library copies the configuration, so it lives in C memory only
	// for the duration of the call.
	var mem cmem
	defer mem.free()
	backend := cnew[C.nemo_speech_asr_backend_config](&mem)
	backend.size = C.sizeof_nemo_speech_asr_backend_config
	backend.gpu = 0 // the first GPU: Metal on macOS
	if spec.CPU {
		backend.gpu = -1
	}
	model := cnew[C.nemo_speech_asr_model_config](&mem)
	model.size = C.sizeof_nemo_speech_asr_model_config
	model.path = mem.str(spec.Path)
	cfg := cnew[C.nemo_speech_asr_recognizer_config](&mem)
	cfg.size = C.sizeof_nemo_speech_asr_recognizer_config
	cfg.backend = backend
	cfg.model = model
	if spec.VADPath != "" {
		vad := cnew[C.nemo_speech_asr_vad_config](&mem)
		vad.size = C.sizeof_nemo_speech_asr_vad_config
		vad.model_path = mem.str(spec.VADPath)
		vad.enable_masking = true // the library loads the VAD only to use it
		cfg.vad = vad
	}

	var rec *C.nemo_speech_asr_recognizer
	var msg *C.char
	if status := C.gs_create(cfg, &rec, &msg); status != C.NEMO_SPEECH_ASR_OK {
		return engine.ModelInfo{}, statusError(op, status, msg)
	}
	if err := ctx.Err(); err != nil {
		C.nemo_speech_asr_destroy(rec)
		return engine.ModelInfo{}, cancelled(op, err)
	}
	h.rec = rec
	h.info = engine.ModelInfo{ID: spec.ID, Streaming: meta.streaming, Languages: meta.languages}
	return cloneInfo(h.info), nil
}

// Unload implements engine.Engine.
func (e *Engine) Unload() error {
	e.h.mu.Lock()
	defer e.h.mu.Unlock()
	e.h.releaseLocked()
	return nil
}

// releaseLocked destroys the recognizer and returns the memory it freed to
// the OS. h.mu must be held for writing.
func (h *handles) releaseLocked() {
	if h.rec == nil {
		return
	}
	C.nemo_speech_asr_destroy(h.rec)
	h.rec, h.info = nil, engine.ModelInfo{}
	C.gs_release_free_memory()
}

// Recognize implements engine.Engine. NeMo-Speech.cpp v0.3.0 cannot stop a
// decode once it has started, so cancelling ctx then only discards the
// result.
func (e *Engine) Recognize(ctx context.Context, samples []float32, sampleRate int, opts engine.Options) (engine.Result, error) {
	const op = "recognize"
	h := e.h
	h.mu.RLock()
	defer h.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return engine.Result{}, cancelled(op, err)
	}
	if h.rec == nil {
		return engine.Result{}, &engine.Error{Op: op, Kind: engine.ErrNotLoaded}
	}
	if len(samples) == 0 {
		return engine.Result{}, invalid(op, "empty audio")
	}
	if err := checkRate(op, sampleRate); err != nil {
		return engine.Result{}, err
	}

	var mem cmem
	defer mem.free()
	var res *C.nemo_speech_asr_result
	var msg *C.char
	// samples is Go memory without pointers, which C may read for the
	// duration of the call; the library does not keep it.
	status := C.gs_recognize_f32(h.rec, mem.options(opts),
		(*C.float)(unsafe.Pointer(&samples[0])), C.size_t(len(samples)), C.int32_t(sampleRate),
		&res, &msg)
	if status != C.NEMO_SPEECH_ASR_OK {
		return engine.Result{}, statusError(op, status, msg)
	}
	defer C.nemo_speech_asr_result_destroy(res)
	if err := ctx.Err(); err != nil {
		return engine.Result{}, cancelled(op, err)
	}
	r := copyResult(res)
	r.Final = true
	return r, nil
}

// StartStream implements engine.Engine. Streaming is not wired up yet, so
// every model is treated as batch-only.
func (e *Engine) StartStream(context.Context, engine.Options) (engine.Stream, error) {
	return nil, invalid("start stream", "streaming recognition is not implemented yet")
}

// copyResult copies the top alternative of res into Go memory.
func copyResult(res *C.nemo_speech_asr_result) engine.Result {
	r := engine.Result{
		Final:          bool(C.nemo_speech_asr_result_is_final(res)),
		AudioProcessed: time.Duration(float64(C.nemo_speech_asr_result_audio_processed(res)) * float64(time.Second)),
	}
	if C.nemo_speech_asr_result_alternative_count(res) == 0 {
		return r
	}
	r.Text = C.GoString(C.nemo_speech_asr_result_transcript(res, 0))
	r.Confidence = float32(C.nemo_speech_asr_result_confidence(res, 0))
	if n := int(C.nemo_speech_asr_result_word_count(res, 0)); n > 0 {
		r.Words = make([]engine.Word, n)
		for i := range r.Words {
			ci := C.size_t(i)
			r.Words[i] = engine.Word{
				Text:       C.GoString(C.nemo_speech_asr_result_word_text(res, 0, ci)),
				Start:      time.Duration(C.nemo_speech_asr_result_word_start_time(res, 0, ci)) * time.Millisecond,
				End:        time.Duration(C.nemo_speech_asr_result_word_end_time(res, 0, ci)) * time.Millisecond,
				Confidence: float32(C.nemo_speech_asr_result_word_confidence(res, 0, ci)),
			}
		}
	}
	for i := range int(C.nemo_speech_asr_result_language_count(res, 0)) {
		r.Languages = append(r.Languages, C.GoString(C.nemo_speech_asr_result_language_code(res, 0, C.size_t(i))))
	}
	return r
}

// cmem tracks C allocations made for one call and frees them together.
type cmem struct{ ptrs []unsafe.Pointer }

// cnew allocates a zeroed T in C memory.
func cnew[T any](m *cmem) *T {
	var zero T
	p := C.calloc(1, C.size_t(unsafe.Sizeof(zero)))
	if p == nil {
		panic("nemo: out of C memory")
	}
	m.ptrs = append(m.ptrs, p)
	return (*T)(p)
}

// str copies s into a C string.
func (m *cmem) str(s string) *C.char {
	p := C.CString(s)
	m.ptrs = append(m.ptrs, unsafe.Pointer(p))
	return p
}

func (m *cmem) free() {
	for _, p := range m.ptrs {
		C.free(p)
	}
	m.ptrs = nil
}

// options converts opts to recognition options in C memory, starting from
// the library's defaults.
func (m *cmem) options(opts engine.Options) *C.nemo_speech_asr_recognition_options {
	o := cnew[C.nemo_speech_asr_recognition_options](m)
	*o = C.nemo_speech_asr_recognition_options_default()
	// Keep the punctuation and casing the model writes. Without this the
	// library lowercases transcripts and strips punctuation, as Riva does;
	// with no PnC model configured it adds none of its own.
	o.enable_automatic_punctuation = true
	if opts.Language != "" {
		o.language_code = m.str(opts.Language)
	}
	o.interim_results = C.bool(opts.InterimResults)
	o.enable_word_time_offsets = C.bool(opts.WordTimestamps)
	if len(opts.Phrases) > 0 {
		phrases := unsafe.Slice(cnewArray[*C.char](m, len(opts.Phrases)), len(opts.Phrases))
		for i, p := range opts.Phrases {
			phrases[i] = m.str(p)
		}
		sc := cnew[C.nemo_speech_asr_speech_context](m)
		sc.size = C.sizeof_nemo_speech_asr_speech_context
		sc.phrases = &phrases[0]
		sc.phrase_count = C.size_t(len(phrases))
		sc.boost = C.float(opts.Boost)
		o.speech_contexts = sc
		o.speech_context_count = 1
	}
	return o
}

// cnewArray allocates n zeroed Ts in C memory.
func cnewArray[T any](m *cmem, n int) *T {
	var zero T
	p := C.calloc(C.size_t(n), C.size_t(unsafe.Sizeof(zero)))
	if p == nil {
		panic("nemo: out of C memory")
	}
	m.ptrs = append(m.ptrs, p)
	return (*T)(p)
}

func invalid(op, detail string) error {
	return &engine.Error{Op: op, Kind: engine.ErrInvalidArgument, Detail: detail}
}

func cancelled(op string, cause error) error {
	return &engine.Error{Op: op, Kind: engine.ErrCancelled, Err: cause}
}

// checkRate rejects sample rates the library cannot resample. 0 means the
// model's own rate.
func checkRate(op string, rate int) error {
	if rate != 0 && (rate < engine.MinSampleRate || rate > engine.MaxSampleRate) {
		return invalid(op, fmt.Sprintf("sample rate %d Hz is outside %d-%d Hz", rate, engine.MinSampleRate, engine.MaxSampleRate))
	}
	return nil
}

func cloneInfo(info engine.ModelInfo) engine.ModelInfo {
	info.Languages = slices.Clone(info.Languages)
	return info
}
