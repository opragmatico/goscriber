# Goscriber

**Offline, private push-to-talk speech-to-text for your desktop — built with Go, Fyne and NVIDIA NeMo-Speech.cpp.**

Press a shortcut, speak, release, and your words are pasted into whatever app you are using. Audio never leaves your computer.

Goscriber is a Go re-implementation of [Handy](https://github.com/cjpais/Handy) by CJ Pais. Handy is a Tauri (Rust + React) app that runs many model families through `transcribe.cpp`. Goscriber keeps Handy's dictation behaviour but deliberately narrows the speech stack to **two NVIDIA models running in-process through [NeMo-Speech.cpp](https://github.com/NVIDIA/NeMo-Speech.cpp) via CGo**, and ships a single native binary with a [Fyne](https://fyne.io) UI.

> **Status: early development.** `goscriber` starts as a menu bar / tray icon with a placeholder settings window; dictation is not wired up yet. `goscriber transcribe` already transcribes WAV files from the command line. See the [roadmap](#roadmap) and the [issue tracker](https://github.com/opragmatico/goscriber/issues).

## Supported models

| Model | Hugging Face | Languages | Mode | Notes |
| --- | --- | --- | --- | --- |
| **Nemotron Streaming 3.5** (0.6B) | [`nvidia/nemotron-3.5-asr-streaming-0.6b`](https://huggingface.co/nvidia/nemotron-3.5-asr-streaming-0.6b) | 40+ locales, `auto` detection | Cache-aware streaming RNNT | Default model. NVIDIA publishes the Q8_0 GGUF for NeMo-Speech.cpp. |
| **Parakeet TDT 0.6B v2** | [`nvidia/parakeet-tdt-0.6b-v2`](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v2) | English | Offline TDT (batch) | Fast, accurate English with punctuation and timestamps. Only a `.nemo` checkpoint is published; Goscriber converts it to GGUF with NeMo-Speech.cpp's converter and [hosts the result](docs/models/gguf.md) on Hugging Face. The runtime officially supports only its v3 sibling, but v2 [converts and runs with the model card's accuracy](docs/models/parakeet-tdt-0.6b-v2.md). |

Goscriber ships **without any model**. On first run you choose one and download it (about 700 MB at the default Q8_0 quantisation); nothing is bundled or downloaded automatically. From the command line:

```bash
goscriber models list                                   # each model, its size and whether it is ready
goscriber models pull nemotron-3.5-asr-streaming-0.6b   # download it (and the Silero VAD); Ctrl-C and rerun to resume
goscriber models rm parakeet-tdt-0.6b-v2                # delete its files
```

Downloads come from Hugging Face at pinned revisions and are checked against their SHA-256. A matching file already in your Hugging Face cache is used from there instead of being downloaded again.

Then transcribe audio files (WAV, any sample rate; run `goscriber transcribe --help` for every flag):

```bash
goscriber transcribe recording.wav                                    # the transcript, on stdout
goscriber transcribe --model parakeet-tdt-0.6b-v2 --cpu a.wav b.wav   # another model, on the CPU
goscriber transcribe --stream --json recording.wav                    # interim text on stderr; words, timings and language as JSON
```

No other models or engines are planned. Whisper, Canary, Moonshine and the rest of Handy's catalog are out of scope.

## How it will work

1. **Press** a global shortcut: hold to record and release to stop (push-to-talk), or tap to toggle.
2. **Speak**. A small overlay shows that Goscriber is listening and, with Nemotron Streaming 3.5, the words as they are recognised.
3. **Release**. NeMo-Speech.cpp finishes the utterance locally on Metal (Apple Silicon) or the CPU.
4. **Get** the text pasted into the focused text field.

Following Handy, Goscriber also plans transcription history, custom vocabulary (backed by NeMo-Speech.cpp's word boosting), filler-word removal, an external transcription hook and optional LLM post-processing.

## Stack

| Concern | Library |
| --- | --- |
| UI, tray, overlay | [Fyne](https://fyne.io) v2 |
| Speech-to-text | [NeMo-Speech.cpp](https://github.com/NVIDIA/NeMo-Speech.cpp) C ABI (`nemo_speech/asr.h`) through CGo |
| Voice activity detection | Silero VAD, run by NeMo-Speech.cpp |
| Audio capture and sounds | [`malgo`](https://github.com/gen2brain/malgo) (miniaudio) |
| Global shortcuts | [`golang.design/x/hotkey`](https://github.com/golang-design/hotkey), plus native code where it falls short |
| Clipboard and paste | [`golang.design/x/clipboard`](https://github.com/golang-design/clipboard) + platform key events |
| Settings | JSON under `os.UserConfigDir()` |
| History | SQLite via [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) (pure Go) |
| Logging | `log/slog` |

See [docs/architecture.md](docs/architecture.md) for the package layout, the CGo engine design, the dictation pipeline and the threading model.

## Roadmap

Work is split into five milestones, tracked as GitHub issues:

1. **M0 · Foundation**: Go module, CI, settings, logging, and a Fyne app shell with a tray icon.
2. **M1 · Speech engine**: NeMo-Speech.cpp SDK build, the CGo binding (offline and streaming), Parakeet TDT 0.6B v2 conversion, the two-model catalog and downloader. At the end of M1, `goscriber transcribe file.wav` works with both models.
3. **M2 · MVP dictation**: microphone, VAD, global shortcut, paste, coordinator, overlay and macOS permissions. At the end of M2 you can dictate into any app.
4. **M3 · Live streaming & usable app**: live transcription in the overlay, language selection, custom vocabulary, engine process isolation, settings window, onboarding, history and the CLI.
5. **M4 · Distribution & platforms**: a signed macOS app bundle and releases, Linux and Windows, launch at login, transcription hook, LLM post-processing.

macOS on Apple Silicon is the first target. Linux and Windows follow in M4.

## Development

### Prerequisites

- Go 1.27 or newer, Git and Make.
- macOS: the Xcode Command Line Tools (`xcode-select --install`).
- Linux: a C compiler and Fyne's system packages, for example on Debian/Ubuntu `sudo apt-get install gcc libgl1-mesa-dev xorg-dev libxkbcommon-dev libwayland-dev`. The tray icon needs a StatusNotifierItem host; on GNOME that means the AppIndicator extension.
- For the NeMo-Speech.cpp engine: CMake 3.26+, Ninja, a C++17 compiler and pkg-config, for example `brew install cmake ninja pkgconf` or `sudo apt-get install build-essential cmake ninja-build pkg-config`. `make deps` builds the pinned SDK (v0.3.0) into `.deps/`; see [docs/engine-sdk.md](docs/engine-sdk.md).

### Common tasks

```bash
make deps                 # download Go modules and build the NeMo-Speech.cpp SDK into .deps/
make build                # build ./goscriber, with the engine once make deps has run
make run ARGS=--version   # build ./goscriber and run it
make run ARGS=--debug     # run the app with debug logging and the tray's Debug menu
make app                  # macOS: assemble dist/Goscriber.app (unsigned development bundle)
make test                 # go test -race ./..., then the CGo-free check
make test-engine          # test the NeMo-Speech.cpp binding against the SDK
make engine-version       # print the SDK's version through CGo
make bench                # time both models on Metal and the CPU over testdata/audio (scripts/bench.sh)
make lint                 # golangci-lint, pinned and installed into ./bin on first use
make fmt                  # gofumpt + goimports
make help                 # every target
```

### Where CGo is allowed

Only `internal/app`, `internal/ui`, `internal/platform` and `internal/engine/nemo` may use CGo (Fyne, native OS code and the NeMo-Speech.cpp binding). Everything else must build and test with `CGO_ENABLED=0`, which keeps most tests fast and portable; `make test-nocgo` enforces it, and CI runs it on macOS and Linux. `cmd/goscriber` links the desktop app only when CGo is on, so a `CGO_ENABLED=0` build is a command-line-only binary. The one exception is `internal/audio`: its malgo (miniaudio) backend sits in a `cgo`-tagged file, so the package, with its ring buffer, levels, recorder and WAV code, still builds and tests without CGo, just with no devices.

`goscriber debug devices` lists the audio devices, and `goscriber debug record [--device NAME] [--seconds 10] out.wav` records the microphone and reports the sample rate, dropped samples and levels.

The NeMo-Speech.cpp binding also needs the SDK from `make deps` and, until it is wired into the app, the `nemo` build tag, so CGo builds keep working without the SDK; `make test-engine` passes it, and `make build` does too once the SDK is there, which gives `goscriber transcribe` its engine. A `goscriber` built without it says so. Its tests that need a real model run when `GOSCRIBER_TEST_MODELS` names a directory holding the model's GGUF. See [docs/engine-sdk.md](docs/engine-sdk.md#model-tests).

Shared test fixtures live in [`testdata/`](testdata/README.md).

### Model files

No model file is committed here. [`scripts/convert-models.sh`](scripts/convert-models.sh) reproduces the Parakeet TDT 0.6B v2 and Silero VAD GGUFs that Goscriber downloads, from NVIDIA's checkpoint and the Silero package, in an isolated Python environment (it needs [uv](https://docs.astral.sh/uv/)), and checks them against the pinned SHA-256. Nemotron Streaming 3.5 comes from NVIDIA as a GGUF. See [docs/models/gguf.md](docs/models/gguf.md).

## Acknowledgements

- Goscriber's design follows [Handy](https://github.com/cjpais/Handy) (MIT, © CJ Pais). No Handy source code is copied; this is an independent implementation in Go.
- Speech recognition runs on [NeMo-Speech.cpp](https://github.com/NVIDIA/NeMo-Speech.cpp) (Apache-2.0, © NVIDIA).
- The models are released by NVIDIA: Nemotron Streaming 3.5 under the OpenMDW License 1.1 (at the revision Goscriber pins), Parakeet TDT 0.6B v2 under CC-BY-4.0. See each model card for terms.
- Voice activity detection uses [Silero VAD](https://github.com/snakers4/silero-vad) (MIT, © Silero Team).

## License

[MIT](LICENSE)
