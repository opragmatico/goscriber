package worker

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"unsafe"

	"github.com/opragmatico/goscriber/internal/engine"
)

// The wire protocol between the supervisor and its worker.
//
// Every message is one frame: a little-endian uint32 length and that many
// bytes of JSON, then a little-endian uint32 length and that many bytes of
// audio, as little-endian float32 samples (usually none). Each request
// carries an ID and gets exactly one response with the same ID. Requests
// may overlap: the worker answers each as it finishes, so a ping is
// answered while a long decode runs.

// protocolVersion changes whenever the messages do. The supervisor and the
// worker are the same executable, but the file can be rebuilt while the app
// runs; hello refuses a worker of another version.
const protocolVersion = 1

// maxSection is the largest JSON or audio section a frame may carry, so a
// garbage length cannot turn into a huge allocation. 1 GiB of float32 audio
// is over an hour and a half at 48 kHz.
const maxSection = 1 << 30

// op is what a request asks for.
type op string

// Requests.
const (
	opHello        op = "hello"           // the first request: versions and the worker's process ID
	opPing         op = "ping"            // heartbeat, answered at once
	opLoad         op = "load"            // Spec
	opUnload       op = "unload"          //
	opRecognize    op = "recognize"       // Options, Rate, audio
	opStreamStart  op = "stream_start"    // Options; answered with Stream
	opStreamPush   op = "stream_push"     // Stream, Rate, audio
	opStreamNext   op = "stream_next"     // Stream; answered with Result, nothing or EOF
	opStreamEnd    op = "stream_endpoint" // Stream: ForceEndpoint
	opStreamFinish op = "stream_finish"   // Stream
	opStreamClose  op = "stream_close"    // Stream
)

// request is a frame from the supervisor to the worker.
type request struct {
	ID      uint64           `json:"id"`
	Op      op               `json:"op"`
	Version int              `json:"version,omitempty"`
	Spec    *engine.LoadSpec `json:"spec,omitempty"`
	Options *engine.Options  `json:"options,omitempty"`
	Rate    int              `json:"rate,omitempty"`
	Stream  uint64           `json:"stream,omitempty"`
}

// response is a frame from the worker to the supervisor.
type response struct {
	ID      uint64            `json:"id"`
	Version int               `json:"version,omitempty"`
	PID     int               `json:"pid,omitempty"`
	Info    *engine.ModelInfo `json:"info,omitempty"`
	Result  *engine.Result    `json:"result,omitempty"`
	Stream  uint64            `json:"stream,omitempty"`
	EOF     bool              `json:"eof,omitempty"`
	Err     *wireError        `json:"error,omitempty"`
}

// wireError is an *engine.Error, or any error, as it crosses the pipe.
type wireError struct {
	Op     string `json:"op,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Detail string `json:"detail,omitempty"`
	Cause  string `json:"cause,omitempty"`
}

// kinds names the engine's error kinds on the wire.
var kinds = map[string]error{
	"invalid_argument":   engine.ErrInvalidArgument,
	"out_of_memory":      engine.ErrOutOfMemory,
	"runtime":            engine.ErrRuntime,
	"cancelled":          engine.ErrCancelled,
	"resource_exhausted": engine.ErrResourceExhausted,
	"unavailable":        engine.ErrUnavailable,
	"not_loaded":         engine.ErrNotLoaded,
	"model_not_found":    engine.ErrModelNotFound,
}

// toWire converts err for the response to op. Errors that are not
// *engine.Error become runtime errors of op.
func toWire(o op, err error) *wireError {
	if err == nil {
		return nil
	}
	eerr, ok := errors.AsType[*engine.Error](err)
	if !ok {
		return &wireError{Op: string(o), Kind: "runtime", Detail: err.Error()}
	}
	w := &wireError{Op: eerr.Op, Detail: eerr.Detail}
	for name, k := range kinds {
		if eerr.Kind == k {
			w.Kind = name
		}
	}
	if w.Kind == "" && eerr.Kind != nil {
		w.Kind = "runtime"
		w.Detail = joinDetail(eerr.Kind.Error(), w.Detail)
	}
	if eerr.Err != nil {
		w.Cause = eerr.Err.Error()
	}
	return w
}

// err turns w back into an *engine.Error, or nil.
func (w *wireError) err() error {
	if w == nil {
		return nil
	}
	e := &engine.Error{Op: w.Op, Kind: kinds[w.Kind], Detail: w.Detail}
	if e.Kind == nil {
		e.Kind = engine.ErrRuntime
	}
	if w.Cause != "" {
		e.Err = errors.New(w.Cause)
	}
	return e
}

func joinDetail(a, b string) string {
	if b == "" {
		return a
	}
	return a + ": " + b
}

// errFrame is a message writeFrame refuses to send, as opposed to a failure
// to write it.
var errFrame = errors.New("cannot send")

// writeFrame writes msg and samples as one frame. It refuses a section the
// reader would refuse, with an error matching errFrame, so an oversized
// request fails here instead of breaking the worker's input.
func writeFrame(w io.Writer, msg any, samples []float32) error {
	js, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("%w: encoding the message: %v", errFrame, err)
	}
	if len(js) > maxSection {
		return fmt.Errorf("%w: a message of %d bytes is too large", errFrame, len(js))
	}
	if len(samples) > maxSection/4 {
		return fmt.Errorf("%w: %d samples are too long for one request", errFrame, len(samples))
	}
	var head [4]byte
	binary.LittleEndian.PutUint32(head[:], uint32(len(js)))
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	if _, err := w.Write(js); err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(head[:], uint32(len(samples)*4))
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	if len(samples) > 0 {
		if _, err := w.Write(sampleBytes(samples)); err != nil {
			return err
		}
	}
	return nil
}

// readFrame reads one frame into msg and returns its audio. It returns
// io.EOF only on a clean end before the first byte of a frame; a frame cut
// off part-way is io.ErrUnexpectedEOF.
func readFrame(r io.Reader, msg any) ([]float32, error) {
	n, err := readLen(r, true)
	if err != nil {
		return nil, err
	}
	js := make([]byte, n)
	if _, err := io.ReadFull(r, js); err != nil {
		return nil, unexpected(err)
	}
	if err := json.Unmarshal(js, msg); err != nil {
		return nil, fmt.Errorf("decoding a message: %w", err)
	}
	n, err = readLen(r, false)
	if err != nil {
		return nil, err
	}
	if n%4 != 0 {
		return nil, fmt.Errorf("audio section of %d bytes is not whole samples", n)
	}
	if n == 0 {
		return nil, nil
	}
	samples := make([]float32, n/4)
	if _, err := io.ReadFull(r, sampleBytes(samples)); err != nil {
		return nil, unexpected(err)
	}
	if !littleEndian {
		swap(samples)
	}
	return samples, nil
}

// readLen reads a section length. At the start of a frame (first) a clean
// end of input is io.EOF.
func readLen(r io.Reader, first bool) (int, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		if first && errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		return 0, unexpected(err)
	}
	n := binary.LittleEndian.Uint32(head[:])
	if n > maxSection {
		return 0, fmt.Errorf("section of %d bytes is too large", n)
	}
	return int(n), nil
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// littleEndian reports whether float32s are laid out in memory as the wire
// wants them, so audio is written and read without converting it.
var littleEndian = binary.NativeEndian.Uint16([]byte{1, 0}) == 1

// sampleBytes returns samples as little-endian bytes: their own memory on a
// little-endian machine, a converted copy otherwise.
func sampleBytes(samples []float32) []byte {
	if len(samples) == 0 {
		return nil
	}
	if littleEndian {
		return unsafe.Slice((*byte)(unsafe.Pointer(&samples[0])), len(samples)*4)
	}
	b := make([]byte, len(samples)*4)
	for i, s := range samples {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(s))
	}
	return b
}

// swap converts samples read as raw bytes on a big-endian machine.
func swap(samples []float32) {
	for i, s := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(
			unsafe.Slice((*byte)(unsafe.Pointer(&s)), 4)))
	}
}

// finite replaces NaN and infinite confidences, which JSON cannot carry,
// with 0.
func finite(r *engine.Result) {
	fix := func(f *float32) {
		if math.IsNaN(float64(*f)) || math.IsInf(float64(*f), 0) {
			*f = 0
		}
	}
	fix(&r.Confidence)
	for i := range r.Words {
		fix(&r.Words[i].Confidence)
	}
}
