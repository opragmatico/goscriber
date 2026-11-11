// Package nemo binds the NeMo-Speech.cpp ASR C ABI (nemo_speech/asr.h) through
// CGo and implements engine.Engine. It is one of the packages allowed to use
// CGo.
//
// An Engine holds one recognizer at a time. Before loading a model it checks
// that the file exists (engine.ErrModelNotFound otherwise) and reads from the
// GGUF metadata what the C ABI does not report: whether the model can stream
// and which languages it takes. Each fallible C call goes through a small C
// shim (shim.h) that copies the library's thread-local error message in the
// same call, because Go may move a goroutine to another OS thread between
// two CGo calls.
//
// The binding links the SDK that `make deps` builds into .deps/nemo-speech,
// which pkg-config finds as nemo-speech-asr. Its CGo files also need the
// nemo build tag, so CGo builds on machines without the SDK keep working:
//
//	go test -tags nemo ./internal/engine/nemo/...
//
// Tests that need a model run when GOSCRIBER_TEST_MODELS names a directory
// holding it. See docs/engine-sdk.md.
package nemo
