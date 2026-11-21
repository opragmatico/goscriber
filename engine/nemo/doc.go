// Package nemo binds the NeMo-Speech.cpp ASR C ABI (nemo_speech/asr.h) through
// CGo and implements engine.Engine. It is one of the packages allowed to use
// CGo.
//
// An Engine holds one recognizer at a time. Before loading a model it checks
// that the file exists (engine.ErrModelNotFound otherwise) and reads from the
// GGUF metadata what the C ABI does not report: whether the model can stream
// and which languages it takes. Streaming models decode live through
// StartStream, looking ahead as far as the LoadSpec's latency preset says;
// a stream is one utterance, with endpointing off.
//
// Each fallible C call goes through a small C shim (shim.h) that copies the
// library's thread-local error message in the same call, because Go may move
// a goroutine to another OS thread between two CGo calls.
//
// # Building
//
// The binding links the SDK that `make deps` builds into .deps/nemo-speech,
// which pkg-config finds as nemo-speech-asr. Its CGo files also need the
// nemo build tag, so CGo builds on machines without the SDK keep working:
//
//	go test -tags nemo ./engine/nemo/...
//
// Without the tag, or without CGo, the package has no New, Version or
// GPUBackend: a build that calls them fails with "undefined: nemo.New".
//
// Tests that need a model run when GOSCRIBER_TEST_MODELS names a directory
// holding it. See docs/engine-sdk.md.
//
// # Using it from another module
//
// Another Go program imports this package and engine, and builds the same
// way: build the SDK once in a Goscriber checkout, point pkg-config at it
// and pass the tag.
//
//	git clone https://github.com/opragmatico/goscriber && cd goscriber
//	make deps
//	export PKG_CONFIG_PATH=$PWD/.deps/nemo-speech/lib/pkgconfig
//
//	cd ../yourapp
//	go get github.com/opragmatico/goscriber@latest
//	go build -tags nemo ./...
//
// The SDK's libraries are shared and the pkg-config file sets an rpath to
// .deps/nemo-speech/lib, so the program finds them on the machine that built
// it; to ship it, bundle the libraries as docs/engine-sdk.md describes.
//
// The engine never downloads anything: LoadSpec.Path names a GGUF on disk,
// such as one that `goscriber models pull` fetched into its cache.
//
// A crash inside NeMo-Speech.cpp (a GGML_ASSERT, an abort, a GPU driver
// fault) ends the process that loaded it. The Goscriber app therefore runs
// the engine in a child process; a program that cannot afford to crash
// should do the same.
package nemo
