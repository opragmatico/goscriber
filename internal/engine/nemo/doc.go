// Package nemo binds the NeMo-Speech.cpp ASR C ABI (nemo_speech/asr.h) through
// CGo and implements engine.Engine. It is one of the packages allowed to use
// CGo.
//
// The binding links the SDK that `make deps` builds into .deps/nemo-speech,
// which pkg-config finds as nemo-speech-asr. Until the binding proper lands,
// its CGo files also need the nemo build tag, so CGo builds on machines
// without the SDK keep working:
//
//	go test -tags nemo ./internal/engine/nemo/...
//
// See docs/engine-sdk.md.
package nemo
