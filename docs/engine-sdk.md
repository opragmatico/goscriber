# The NeMo-Speech.cpp SDK

Goscriber runs speech recognition in-process through the [NeMo-Speech.cpp](https://github.com/NVIDIA/NeMo-Speech.cpp) C ABI (`nemo_speech/asr.h`), linked with CGo. This page covers how that SDK is pinned and built, how Go links it, and what the linking choice means for packaging.

## Quick start

```bash
# macOS on Apple Silicon
xcode-select --install
brew install cmake ninja pkgconf

# Debian / Ubuntu
sudo apt-get install -y build-essential cmake ninja-build git pkg-config

make deps            # builds .deps/nemo-speech: about a minute the first time, a second after that
make engine-version  # prints 0.3.0 through CGo
make test-engine     # vets and tests internal/engine/nemo against the SDK
```

## Prerequisites

- Git, **CMake 3.26** or newer, **Ninja**, and a C and C++17 compiler: the Xcode Command Line Tools on macOS, GCC or Clang on Linux.
- **pkg-config** (or pkgconf), which the Go build uses to find the SDK. macOS does not ship one.
- No SentencePiece or Abseil packages. Upstream's build guide lists them because its default build links the system SentencePiece; Goscriber builds the SentencePiece version NeMo-Speech.cpp pins and links it statically (see [below](#what-make-deps-builds)).

## What `make deps` builds

`make deps` downloads the Go modules and runs `scripts/build-nemo-speech.sh`:

- **Source**: `third_party/nemo-speech`, a shallow git submodule pinned to tag `v0.3.0` (commit `20999cc9`). On first use the script checks it out together with its `llama.cpp` submodule, which also provides ggml. It fetches no other upstream submodules.
- **Preset**: upstream's `metal-asr` on Apple Silicon and `cpu-asr` on Linux. `NEMO_SPEECH_PRESET` overrides it.
- **ASR only**: diarization, TTS, translation, speech-LLM, VoiceChat, the CLI and its microphone capture, HTTP, gRPC, examples, tests and tools are all off. Text normalisation (`NEMO_SPEECH_WITH_NORM`, Sparrowhawk/OpenFST) is off too; upstream leaves it off on macOS. CUDA and Vulkan presets wait for the Linux and Windows issues.
- **SentencePiece**: upstream's pinned static build (`scripts/build_sentencepiece_static.sh`), linked into `libnemo_speech_asr` with its symbols hidden, as in upstream's release archives. The SDK therefore needs no SentencePiece, Abseil or protobuf at run time.
- **macOS 13.3** minimum (`CMAKE_OSX_DEPLOYMENT_TARGET`), the same as upstream's release archives. Without it, CMake would target the build machine's macOS version. Set `MACOSX_DEPLOYMENT_TARGET` to change it.
- **Install**: `cmake --install` into `.deps/nemo-speech`, plus a generated `lib/pkgconfig/nemo-speech-asr.pc`.

`NEMO_SPEECH_CMAKE_ARGS` passes extra CMake arguments, for example `-DGGML_NATIVE=OFF`.

```text
.deps/
├── nemo-speech/                     # the SDK: what Go links and what CI caches
│   ├── include/nemo_speech/asr.h
│   ├── lib/
│   │   ├── libnemo_speech_asr_c.*   # the stable C ABI
│   │   ├── libnemo_speech_asr.*     # the ASR runtime behind it
│   │   ├── libggml*.*               # ggml and its backends (CPU; Metal and BLAS on macOS)
│   │   ├── pkgconfig/nemo-speech-asr.pc
│   │   └── cmake/NeMoSpeech/        # upstream's CMake package, unused by Go
│   └── share/                       # licences, upstream docs, the build stamp
└── build/                           # SentencePiece and CMake build trees; safe to delete
```

### Skipping work

The install records a build stamp in `share/goscriber/build-stamp`: the pinned commit, the platform, the preset, the CMake options and a recipe number for the script itself. When the installed stamp matches, `make deps` only rewrites the pkg-config file, in case the checkout moved, and finishes in about a second. Otherwise it rebuilds (incrementally, in `.deps/build`), installs into a staging directory and swaps that in, so a failed build never leaves a half-installed SDK behind.

- `scripts/build-nemo-speech.sh --force` rebuilds anyway; `make clean-deps` removes `.deps/`.
- Another checkout or git worktree at the same pin can reuse an SDK: copy `.deps/nemo-speech` across, and `make deps` adopts it.

## Linking from Go

`internal/engine/nemo` declares `#cgo pkg-config: nemo-speech-asr`. The generated file gives:

```text
Cflags: -I<prefix>/include
Libs:   -L<prefix>/lib -Wl,-rpath,<prefix>/lib -lnemo_speech_asr_c
```

The rpath lets development binaries and `go test` run straight from the tree, without `DYLD_LIBRARY_PATH` or `LD_LIBRARY_PATH`. Go needs to find the file:

- `make` targets export `PKG_CONFIG_PATH` with `.deps/nemo-speech/lib/pkgconfig` first.
- Plain `go` commands need it exported: `export PKG_CONFIG_PATH="$PWD/.deps/nemo-speech/lib/pkgconfig"`.

**Build tag.** The files that need the SDK also carry the `nemo` build tag (`//go:build cgo && nemo`), so `go build ./...`, `go test ./...` and the `check` CI job keep working on machines without the SDK. Build or test them with `-tags nemo`, as `make test-engine` does. The tag stays until the engine is wired into the app: until then nothing outside `internal/engine/nemo` links the SDK, and dropping the tag would make every `go test ./...` need `make deps` first.

**Version.** `nemo_speech_asr_version()` returns `"nemo-speech-asr 0.3.0"`; `nemo.Version()` returns `"0.3.0"`. `make engine-version` prints it, and `TestVersionMatchesSDK` checks that the library the test binary loads is the one the pkg-config file describes. `Engine.Load` refuses a runtime that is not compatible with the version the binding was written for (`sdkVersion` in `internal/engine/nemo/version.go`): the same major version and, while that is 0, the same minor version.

## Decision: shared libraries, not static linking

Goscriber links the SDK's shared libraries, which is the layout upstream documents and installs: `libnemo_speech_asr_c`, `libnemo_speech_asr` and the ggml libraries. The one static piece is SentencePiece, inside `libnemo_speech_asr`.

- **It is upstream's contract.** `libnemo_speech_asr_c` is the stable binary interface: it is versioned (SOVERSION 1) and built with hidden visibility, so it exports only the `nemo_speech_*` C functions. Upstream declares both ASR libraries `SHARED` outright and installs ggml and each backend as separate shared libraries. Linking statically would mean patching upstream's CMake on every bump, then rebuilding its link graph (ggml backends, the Metal and Accelerate frameworks, OpenMP, SentencePiece) by hand in CGo flags.
- **The C++ stays behind the C ABI.** The C++ runtime, ggml and SentencePiece symbols stay inside the libraries. Linked statically, they would share one symbol namespace with everything else CGo links into the binary (GLFW through Fyne, miniaudio later).
- **Backends stay swappable.** ggml can choose CPU variants and GPU backends at run time from separate libraries (`GGML_BACKEND_DL`), which releases and the CUDA and Vulkan builds will want. Static linking rules that out.

The cost is that the binary is not self-contained. It needs the libraries at run time, and releases must carry them.

## Packaging consequences

All platforms:

- Keep the libraries together. They find each other through relative rpaths (`@loader_path`, `$ORIGIN`), so the release only has to point the binary at that one directory.
- Replace the development rpath. Binaries built from the tree carry an absolute rpath into `.deps/nemo-speech/lib`. Go's CGo flag allowlist rejects `-Wl,-rpath,@…` in `#cgo` and pkg-config flags, so release builds either rewrite the rpath after `go build` or pass the release one through the `CGO_LDFLAGS` environment variable, which the allowlist does not check.
- Build portable CPU code. Development builds let ggml tune its CPU code for the build machine (`GGML_NATIVE`). Releases must build with `-DGGML_NATIVE=OFF`, as CI does, or with ggml's run-time-selected CPU variants, or they can crash with illegal instructions on older CPUs.
- Ship the licences in `share/licenses/nemo-speech/`: NeMo-Speech.cpp (Apache-2.0, with its NOTICE and third-party notices), ggml (MIT), miniaudio, and SentencePiece (Apache-2.0) with the Abseil, protobuf-lite and darts-clone code it bundles.

macOS (the release bundle, [#34](https://github.com/opragmatico/goscriber/issues/34)):

- Copy the libraries (about 6 MB) into `Goscriber.app/Contents/Frameworks/`, resolving the version symlinks to the names the binaries reference (`libnemo_speech_asr_c.1.dylib`, `libnemo_speech_asr.dylib`, `libggml*.0.dylib`).
- Give `goscriber` an `@executable_path/../Frameworks` rpath and delete the development one, for example with `install_name_tool -add_rpath` and `-delete_rpath`.
- Sign each library with the hardened runtime before signing the bundle; notarisation then covers them.
- The Metal shaders are embedded in `libggml-metal` (`GGML_METAL_EMBED_LIBRARY`), so there is no `.metallib` to ship.
- `LSMinimumSystemVersion` must be at least 13.3, the libraries' deployment target.
- At run time the libraries need only the system's libc++ and frameworks (Metal, MetalKit, Foundation, Accelerate).

Linux (M4):

- Ship `lib/` beside the binary, for example `bin/goscriber` and `lib/*.so*` in a tarball or AppImage, and set the binary's RUNPATH to `$ORIGIN/../lib` (for example with `patchelf`).
- The libraries need glibc, libstdc++ and libgomp (ggml's CPU backend uses OpenMP with GCC) from the build machine's generation or newer, so build releases on the oldest distribution Goscriber supports.

## Updating the pin

```bash
git -C third_party/nemo-speech fetch --depth 1 origin tag vX.Y.Z
git -C third_party/nemo-speech checkout vX.Y.Z
git add third_party/nemo-speech
make deps          # the stamp names the commit, so this rebuilds
make test-engine
```

Then update `sdkVersion` in `internal/engine/nemo/version.go` and the version this page and the README mention, and run the model tests below. The binding also reads a few GGUF metadata keys the converter writes (`internal/engine/nemo/model.go`); they are not part of the C ABI, so check they still exist. The CI cache key follows the new commit on its own.

## Streaming

`Engine.StartStream` drives `nemo_speech_asr_streaming_recognize` and its `stream_*` calls, with endpointing off: a stream is one utterance, which ends with `Finish` (or `ForceEndpoint`). What the binding relies on, as of v0.3.0:

- **Push copies.** `stream_push_f32` copies the samples into the stream's buffer (or the resampler's) before it returns, so callers may reuse their slice at once. The rate is fixed by the first push; the library resamples 8–96 kHz.
- **Next decodes.** Push only buffers; each `stream_next` decodes what it can and returns one result, or none. `stream_finish` decodes the remaining audio itself and leaves the end-of-stream final for the next `stream_next`.
- **Interims always come.** The C ABI does not read `interim_results`, so the binding drops interim results itself when a stream was started without `InterimResults`.
- **Offline models fail with a runtime error.** `streaming_recognize` on Parakeet TDT reports `NEMO_SPEECH_ASR_ERROR_RUNTIME`; the binding checks the GGUF's `cache_supported` first and returns `ErrInvalidArgument`, which callers treat as "batch only".

The latency presets set `rnnt_right_context`, the encoder frames (80 ms each) the model looks ahead of each chunk. Nemotron 3.5 is trained for 0, 1, 3, 6 and 13, and its GGUF records 3 as the default. `-1` keeps that default: despite upstream's "model max" wording, the library replaces `-1` with the GGUF's value, not the largest the model supports.

| Preset | Right context | Chunk | Metal: Finish → final | CPU: Finish → final | Interims over the 11 s fixture |
| --- | --- | --- | --- | --- | --- |
| `low` | 1 | 160 ms | 17 ms | 26 ms | 63 |
| `balanced` (default) | -1 (3) | 320 ms | 22 ms | 37 ms | 31 |
| `accurate` | 13 | 1.12 s | 46 ms | 94 ms | 9 |

Measured on an M4 Max, best of three, streaming `testdata/audio/jfk.wav` in 20 ms chunks; every preset transcribes it without error.

## Model tests

Most of the binding's tests need only the SDK. The ones that need a real model run when `GOSCRIBER_TEST_MODELS` names a directory holding NVIDIA's GGUF, and skip otherwise:

| File | Source | Size |
| --- | --- | --- |
| `nemotron-3.5-asr-streaming-0.6b.q8_0.gguf` | [`nvidia/nemotron-3.5-asr-streaming-0.6b`](https://huggingface.co/nvidia/nemotron-3.5-asr-streaming-0.6b) | 742 MB |

```bash
GOSCRIBER_TEST_MODELS=~/Library/Caches/goscriber/models make test-engine
```

They transcribe `testdata/audio/jfk.wav` on Metal and on the CPU and keep the word error rate under 10%, check that transcripts keep the model's punctuation and casing, run recognitions concurrently and across `Unload`, and load, recognize and unload the model 100 times (`GOSCRIBER_TEST_CYCLES` changes the count; `-short` skips it). The streaming tests feed the fixture in 20 ms chunks, check that interims arrive and that the final matches the offline result, measure each latency preset, and hold Finish → final under 300 ms on Apple Silicon (without `-race`, whose timings mean nothing). A Parakeet TDT GGUF (`parakeet-tdt-0.6b-v2.q8_0.gguf`, or NVIDIA's `parakeet-tdt-0.6b-v3.q8_0.gguf`) in the same directory adds the check that offline models refuse to stream.

That last test watches the process's memory. `Unload` destroys the recognizer and then asks the C allocator to hand its free memory back to the OS (`malloc_zone_pressure_relief` on macOS, `malloc_trim` with glibc). On macOS the test measures the physical footprint, which is what the system charges the process and Activity Monitor shows; RSS still drifts up by about a hundred megabytes over the cycles because the malloc zones keep clean, reusable pages mapped, which the system reclaims when it needs them. Elsewhere it measures RSS.

## CI

The `engine` job in [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) runs on `macos-latest` (Metal) and `ubuntu-latest` (CPU):

1. It restores `.deps/nemo-speech` from a cache keyed by the runner image, the architecture and a hash of `scripts/build-nemo-speech.sh --stamp`.
2. On a miss it installs Ninja and runs `make deps` with `-DGGML_NATIVE=OFF`, because the cache may be restored on a runner with a different CPU model, then saves the SDK. On a hit, `make deps` only confirms the stamp.
3. It lints, vets and tests `internal/engine/nemo` with `-tags nemo`, then checks that the version `make engine-version` prints through CGo matches the SDK's.
