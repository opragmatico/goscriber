// Package engine defines the speech-recognition interface the rest of
// Goscriber uses: Engine loads one model and recognizes whole clips, Stream
// decodes audio while it is being recorded, and Result carries what either
// produces.
//
// The package has no CGo. The NeMo-Speech.cpp binding lives in engine/nemo,
// the crash-isolating worker in engine/worker, and engine/fake is a scripted
// Engine for tests and for builds without the native engine. Code that only
// needs speech recognition depends on this package and is handed one of
// those, so the coordinator, the CLI and the UI never see CGo.
//
// # Models and decoding
//
// Load reports what the model can do in a ModelInfo. Streaming models decode
// live while the user speaks (StartStream, then Push and Next); the others
// decode the whole clip once recording stops (Recognize). ModelInfo.DecodeMode
// makes that choice.
//
// # Errors
//
// Failures are *Error values whose Kind is one of the Err* sentinels, so
// callers test them with errors.Is. The sentinels mirror NeMo-Speech.cpp's
// status codes, plus ErrNotLoaded for calls made before Load and
// ErrModelNotFound for a model that has not been downloaded.
package engine
