// Package engine defines Goscriber's speech-recognition interface: Engine
// loads one model and recognizes whole clips, Stream decodes audio while it
// is being recorded, and Result carries what either produces.
//
// It is a public package, so other Go programs can use Goscriber's speech
// recognition without the app: import this package for the types and
// engine/nemo for the implementation. See engine/nemo for an example, and
// docs/engine-sdk.md for the native SDK the implementation needs.
//
// The package has no CGo. The NeMo-Speech.cpp binding lives in engine/nemo,
// and engine/fake is a scripted Engine for tests and for builds without the
// native engine. Code that only needs speech recognition depends on this
// package and is handed one of those, so it never sees CGo. Inside the app,
// the crash-isolating worker (internal/engine/worker), the idle unloader and
// the warm-up wrap an Engine and implement the same interface.
//
// # Models and decoding
//
// Load reports what the model can do in a ModelInfo. Streaming models decode
// live while the user speaks (StartStream, then Push and Next); the others
// decode the whole clip once recording stops (Recognize). ModelInfo.DecodeMode
// makes that choice. Both take mono float32 samples in [-1, 1]; decoding
// audio files and capturing a microphone are left to the caller.
//
// # Errors
//
// Failures are *Error values whose Kind is one of the Err* sentinels, so
// callers test them with errors.Is. The sentinels mirror NeMo-Speech.cpp's
// status codes, plus ErrNotLoaded for calls made before Load and
// ErrModelNotFound for a model file that does not exist.
package engine
