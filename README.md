# Goscriber

**Offline, private push-to-talk speech-to-text for your desktop — built with Go, Fyne and NVIDIA NeMo-Speech.cpp.**

Press a shortcut, speak, release, and your words are pasted into whatever app you are using. Audio never leaves your computer.

Goscriber is a Go re-implementation of [Handy](https://github.com/cjpais/Handy) by CJ Pais. Handy is a Tauri (Rust + React) app that runs many model families through `transcribe.cpp`. Goscriber keeps Handy's dictation behaviour but deliberately narrows the speech stack to **NVIDIA models running locally through [NeMo-Speech.cpp](https://github.com/NVIDIA/NeMo-Speech.cpp) via CGo**, and ships a single native binary with a [Fyne](https://fyne.io) UI. As in Handy, the speech engine runs in a supervised worker process (the same binary), so a crash in the native code restarts the engine instead of closing the app.

> **Status: early development.** `goscriber` runs as a menu bar / tray icon: the shortcut or the tray records, transcribes while you speak (Nemotron shows the words in the overlay as you say them; Parakeet decodes the recording in chunks at your pauses), and pastes the text the moment you stop. The settings window (Settings… in the tray) downloads and selects the model, records the shortcut and holds every other setting; at the first launch it walks you through the permissions, downloading a model and a first dictation. `goscriber transcribe` transcribes WAV files from the command line. See the [roadmap](#roadmap) and the [issue tracker](https://github.com/opragmatico/goscriber/issues).

## Supported models

| Model | Hugging Face | Languages | Mode | Notes |
| --- | --- | --- | --- | --- |
| **Nemotron Streaming 3.5** (0.6B) | [`nvidia/nemotron-3.5-asr-streaming-0.6b`](https://huggingface.co/nvidia/nemotron-3.5-asr-streaming-0.6b) | 40+ locales, `auto` detection | Cache-aware streaming RNNT | Default model. NVIDIA publishes the Q8_0 GGUF for NeMo-Speech.cpp. |
| **Parakeet TDT 0.6B v2** | [`nvidia/parakeet-tdt-0.6b-v2`](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v2) | English | Offline TDT (batch) | Fast, accurate English with punctuation and timestamps. Only a `.nemo` checkpoint is published; Goscriber converts it to GGUF with NeMo-Speech.cpp's converter and [hosts the result](docs/models/gguf.md) on Hugging Face. The runtime officially supports only its v3 sibling, but v2 [converts and runs with the model card's accuracy](docs/models/parakeet-tdt-0.6b-v2.md). |
| **Parakeet TDT 0.6B v3** | [`nvidia/parakeet-tdt-0.6b-v3`](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3) | 25 European languages, detected by the model | Offline TDT (batch) | v2's multilingual sibling, with punctuation and timestamps. NVIDIA publishes the Q8_0 GGUF for NeMo-Speech.cpp. Slightly less accurate than v2 in English; see [the checks](docs/models/parakeet-tdt-0.6b-v3.md). |

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
goscriber transcribe --llm talk.wav                                   # cleaned by the language model set up in Post-processing
```

No other models or engines are planned. Whisper, Canary, Moonshine and the rest of Handy's catalog are out of scope.

## How it will work

1. **Press** a global shortcut: hold to record and release to stop (push-to-talk), or tap to toggle.
2. **Speak**. A small overlay shows that Goscriber is listening and the words as they are recognised: with Nemotron Streaming 3.5 within a fraction of a second, with Parakeet a sentence or so at a time.
3. **Release**. NeMo-Speech.cpp finishes the utterance locally on Metal (Apple Silicon), Vulkan (Linux builds that include it) or the CPU.
4. **Get** the text pasted into the focused text field.

Names and jargon listed in the `custom_words` setting come out spelled your way: Nemotron is asked to favour them (NeMo-Speech.cpp's word boosting), and Parakeet's transcripts are corrected after the fact ([details](docs/architecture.md#custom-vocabulary)).

Filler words ("um", "uh") are removed, with the punctuation around them, and the `filler_word_removal` and `custom_filler_words` settings turn that off or change the list ([details](docs/architecture.md#text-cleanup)).

Following Handy, Goscriber keeps a history of past dictations with their recordings (copy, paste again, play, transcribe again), transcribes WAV and FLAC files dropped on its History page, however long, can have a language model clean each transcript before it is pasted, or only those dictated with a second shortcut (Gemini, OpenAI, Claude, OpenRouter, Groq, Ollama, or any OpenAI-compatible server such as LM Studio; see [docs/llm-post-processing.md](docs/llm-post-processing.md)), and can send every transcription to a [webhook](#webhook) of yours.

## Webhook

Set a URL in Settings → Advanced → Webhook (the `webhook_url` setting), and Goscriber posts every finished transcription to it as JSON: each dictation, after it is pasted, and each file transcribed on the History page. It is off by default: with no URL, nothing leaves the computer.

```http
POST /transcriptions HTTP/1.1
Content-Type: application/json
webhook-id: 6f1c2b8e-3d4a-4f5b-9a7c-2e8d1f0b3c4a
User-Agent: Goscriber/0.4.0

{
  "transcription_id": 42,
  "text": "Send the report by Friday.",
  "raw_text": "send the report by friday",
  "model": "parakeet-tdt-0.6b-v2",
  "language": "en-US",
  "duration_ms": 2480,
  "started_at": "2026-11-20T08:30:15.123Z",
  "source": "dictation"
}
```

- **Fields.** `transcription_id` is the transcription's entry in the history, the number the History page shows on each entry while a webhook is set. `text` is what was pasted, after cleanup and any [LLM post-processing](docs/llm-post-processing.md); `raw_text` is the model's transcript; `language` is `""` when unknown; `started_at` is when recording started (for a file, when its transcription did), in UTC; `source` is `dictation` or the audio file's name.
- **At least once.** Each POST is a job in a local queue ([River](https://riverqueue.com) on SQLite, in `jobs.sqlite3` beside the settings), so a transcription waits while the server is down or Goscriber is closed, and goes after the next launch. An attempt cut short, by quitting mid-request for instance, is made again, so the same POST can arrive twice: drop repeats by the `webhook-id` header, as [Standard Webhooks](https://www.standardwebhooks.com) names it. It is a random UUID made when the transcription is queued, the same on every attempt and unique to that delivery. Do not drop repeats by `transcription_id`: every machine numbers its own entries, so two can send the same number.
- **Retries.** A network error, no answer within 30 s, a 408, a 429 or a 5xx answer is tried again after attempt⁴ seconds (1 s, 16 s, 81 s, …), up to 14 attempts in about a day. Any other answer outside 2xx (401, 404, a redirect other than 307 or 308) is not retried. A transcription Goscriber gives up raises one notice, which names the URL's host, never the URL, as it may hold a token.
- **IDs.** IDs only go up. With a webhook set, a transcription gets its number even when the history keeps no dictations (`history_limit` 0): it is saved hidden and removed at once. Without a webhook, nothing is saved then. IDs start again from 1 only if the history database, `history.sqlite3`, is deleted.
- **Changes.** A new URL applies to the transcriptions that follow; those already queued keep the URL they were queued for. A delivered transcription is deleted from the jobs database a minute later, and one given up a week later.

## Stack

| Concern | Library |
| --- | --- |
| UI, tray, overlay | [Fyne](https://fyne.io) v2 |
| Speech-to-text | [NeMo-Speech.cpp](https://github.com/NVIDIA/NeMo-Speech.cpp) C ABI (`nemo_speech/asr.h`) through CGo |
| Voice activity detection | Silero VAD, run by NeMo-Speech.cpp |
| Audio capture and sounds | [`malgo`](https://github.com/gen2brain/malgo) (miniaudio) |
| Global shortcuts | [`golang.design/x/hotkey`](https://github.com/golang-design/hotkey) (needs the Accessibility permission on macOS); on Wayland, the XDG GlobalShortcuts portal through [`godbus/dbus`](https://github.com/godbus/dbus), or a desktop shortcut that sends `SIGUSR2` |
| Clipboard and paste | `NSPasteboard` and Core Graphics key events through CGo on macOS (needs the Accessibility permission); X11 selections and XTEST key events through CGo on Linux, or wtype, dotool or ydotool on Wayland |
| Settings | JSON under `os.UserConfigDir()` |
| History | SQLite via [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) (pure Go) |
| Webhook delivery | [River](https://riverqueue.com) job queue on its SQLite driver (`riversqlite`), over `modernc.org/sqlite` |
| Audio files | WAV by our own reader, FLAC via [`mewkiz/flac`](https://github.com/mewkiz/flac) (pure Go) |
| Logging | `log/slog` |

See [docs/architecture.md](docs/architecture.md) for the package layout, the CGo engine design, the dictation pipeline and the threading model.

## Roadmap

Work is split into five milestones, tracked as GitHub issues:

1. **M0 · Foundation**: Go module, CI, settings, logging, and a Fyne app shell with a tray icon.
2. **M1 · Speech engine**: NeMo-Speech.cpp SDK build, the CGo binding (offline and streaming), Parakeet TDT 0.6B v2 conversion, the two-model catalog and downloader. At the end of M1, `goscriber transcribe file.wav` works with both models.
3. **M2 · MVP dictation**: microphone, VAD, global shortcut, paste, coordinator, overlay and macOS permissions. At the end of M2 you can dictate into any app.
4. **M3 · Live streaming & usable app**: live transcription in the overlay, language selection, custom vocabulary, engine process isolation, settings window, onboarding, history and the CLI.
5. **M4 · Distribution & platforms**: a signed macOS app bundle and releases, Linux and Windows, launch at login, transcription webhook, LLM post-processing.

macOS on Apple Silicon is the first target. Linux works on X11 and Wayland desktops, with a little setup on Wayland: see [docs/linux.md](docs/linux.md). Windows is postponed.

## Development

### Prerequisites

- Go 1.27 or newer, Git and Make.
- macOS: the Xcode Command Line Tools (`xcode-select --install`).
- Linux: a C compiler and Fyne's system packages, for example on Debian/Ubuntu `sudo apt-get install gcc libgl1-mesa-dev xorg-dev libxkbcommon-dev libwayland-dev libxtst-dev`. The tray icon needs a StatusNotifierItem host; on GNOME that means the AppIndicator extension, which Ubuntu turns on. Wayland needs a typing tool, and a desktop shortcut where the desktop has no global shortcuts portal (GNOME 46, Sway): [docs/linux.md](docs/linux.md).
- For the NeMo-Speech.cpp engine: CMake 3.26+, Ninja, a C++17 compiler and pkg-config, for example `brew install cmake ninja pkgconf` or `sudo apt-get install build-essential cmake ninja-build pkg-config`. `make deps` builds the pinned SDK (v0.3.0) into `.deps/`, for the CPU on Linux, where `NEMO_SPEECH_PRESET=vulkan-asr make deps` adds the GPU; see [docs/engine-sdk.md](docs/engine-sdk.md).

### Common tasks

```bash
make deps                 # download Go modules and build the NeMo-Speech.cpp SDK into .deps/
make build                # build ./goscriber, with the engine once make deps has run
make run ARGS=--version   # build ./goscriber and run it
make run ARGS=--debug     # run the app with debug logging and the tray's Debug menu
./goscriber --toggle-transcription  # start or stop dictation in the running app (also --cancel)
make app                  # macOS: assemble dist/Goscriber.app (ad-hoc signed development bundle)
make dist-linux           # Linux: a tarball and a .deb with the engine's libraries (docs/linux.md)
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

### macOS permissions

Dictation needs the Microphone and Accessibility permissions. Run from a terminal (`make run`), the terminal app asks for and holds them. Run as `make app && open dist/Goscriber.app`, Goscriber asks for itself. macOS ties the grant to the bundle's signature, which the default ad-hoc signing changes on every build; `make app CODESIGN_IDENTITY="Apple Development: …"` keeps it across rebuilds. See [docs/permissions.md](docs/permissions.md).

### Model files

No model file is committed here. [`scripts/convert-models.sh`](scripts/convert-models.sh) reproduces the Parakeet TDT 0.6B v2 and Silero VAD GGUFs that Goscriber downloads, from NVIDIA's checkpoint and the Silero package, in an isolated Python environment (it needs [uv](https://docs.astral.sh/uv/)), and checks them against the pinned SHA-256. Nemotron Streaming 3.5 comes from NVIDIA as a GGUF. See [docs/models/gguf.md](docs/models/gguf.md).

## Acknowledgements

- Goscriber is a Go re-implementation of [Handy](https://github.com/cjpais/Handy) (MIT, © CJ Pais). The main difference is speech recognition: Goscriber runs three NVIDIA models through NeMo-Speech.cpp, where Handy runs many model families through transcribe.cpp.
- Speech recognition runs on [NeMo-Speech.cpp](https://github.com/NVIDIA/NeMo-Speech.cpp) (Apache-2.0, © NVIDIA).
- The models are released by NVIDIA: Nemotron Streaming 3.5 under the OpenMDW License 1.1 (at the revision Goscriber pins), Parakeet TDT 0.6B v2 under CC-BY-4.0. See each model card for terms.
- Voice activity detection uses [Silero VAD](https://github.com/snakers4/silero-vad) (MIT, © Silero Team).

## License

[MIT](LICENSE)
