# Architecture

Goscriber is a Go re-implementation of [Handy](https://github.com/cjpais/Handy): press a shortcut, speak, and the transcription is pasted into whatever app has focus, fully offline.

Handy is built with Tauri (Rust backend + React UI) and runs a broad catalog of GGUF models through `transcribe.cpp`. Goscriber keeps the same product behaviour, swaps the stack for Go + Fyne, and narrows speech recognition to two NVIDIA models served by [NeMo-Speech.cpp](https://github.com/NVIDIA/NeMo-Speech.cpp) through CGo.

| Concern | Handy | Goscriber |
| --- | --- | --- |
| UI | Tauri webview + React | Fyne v2 widgets |
| Tray / overlay | Tauri tray, NSPanel / webview overlay | Fyne `desktop.App` tray menu; borderless Fyne window, made non-activating and always-on-top through native window handles |
| Speech-to-text | `transcribe-cpp` (many families) | NeMo-Speech.cpp C ABI (`nemo_speech/asr.h`) via CGo, two models only |
| Engine isolation | `engine_supervisor`: worker child process | Same idea: the `goscriber` binary re-launched as an engine worker |
| Model download | Catalog + downloader with mirrors | Embedded two-model catalog, resumable HTTP download, SHA-256 verification |
| Voice activity detection | Silero VAD v6 (GGUF) | Silero VAD GGUF loaded by NeMo-Speech.cpp (feature masking, endpointing) |
| Audio capture | `handy-recorder` (cpal) | `malgo` (miniaudio); NeMo-Speech.cpp resamples 8–96 kHz input itself |
| Global shortcuts | `handy-keys` (CGEventTap) / `rdev` | `golang.design/x/hotkey` (a CGEventTap on macOS, XGrabKey on X11) |
| Paste / typing | `enigo`, platform paste code | `golang.design/x/clipboard` + platform key events (CGo on macOS) |
| Settings | `tauri-plugin-store` (JSON) | Go structs persisted as JSON under `os.UserConfigDir()` |
| History | SQLite (`rusqlite`) | SQLite (`modernc.org/sqlite`, pure Go) + WAV files |
| LLM post-processing | OpenAI-compatible HTTP clients | `net/http` OpenAI-compatible client |
| Single instance / CLI | `tauri-plugin-single-instance`, `clap` | Unix socket / named pipe, stdlib `flag` |
| Sound effects | `rodio` | `malgo` playback |

## Models

| | Nemotron Streaming 3.5 | Parakeet TDT 0.6B v2 |
| --- | --- | --- |
| Source | [`nvidia/nemotron-3.5-asr-streaming-0.6b`](https://huggingface.co/nvidia/nemotron-3.5-asr-streaming-0.6b) | [`nvidia/parakeet-tdt-0.6b-v2`](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v2) |
| Architecture | Cache-aware FastConformer-RNNT with language-ID prompts | FastConformer-TDT (token-and-duration transducer), full attention |
| Languages | 40+ locales, or `auto` | English, with punctuation and capitalisation |
| Decoding | Streaming with interim results | Offline only (NeMo-Speech.cpp rejects streaming for TDT); decoded in chunks of up to about 30 s split at pauses, because some longer inputs come back with stretches missing ([spike](models/parakeet-tdt-0.6b-v2.md#long-audio)) |
| GGUF | Published by NVIDIA (`*.q8_0.gguf`, ~742 MB) | Converted by us with NeMo-Speech.cpp's `convert_model.py`, then hosted with a pinned revision |
| License | OpenMDW 1.1 (at the pinned revision) | CC-BY-4.0 |
| Live text | Interim results in the overlay | Optional chunked decode at pauses while recording |

Handy's own GGUFs (`handy-computer/*-gguf`) target `transcribe.cpp`, not NeMo-Speech.cpp, so Goscriber does not reuse them.

**No model is bundled.** The app, its release artifacts and the repository contain no weights. Settings start with no selected model; the user chooses one in onboarding or on the Models page and downloads it, and the Silero VAD companion comes with that first download. Until then the shortcut and tray point to the download step instead of recording.

## Package layout

```text
cmd/goscriber/             # main: GUI by default; subcommands `transcribe`, `engine-worker`
internal/
├── app/                   # Fyne app bootstrap and object wiring
├── cli/                   # flags, subcommands, remote-control client
├── instance/              # single-instance socket server
├── config/                # paths and settings (JSON, defaults, migrations)
├── logging/               # slog setup, file sink, in-memory ring for the log viewer
├── notice/                # one place to report background errors to the UI
├── audio/
│   ├── capture.go         # Recorder: start / stop, Frames, levels, pre-roll, device fallback
│   ├── backend_malgo.go   # malgo (miniaudio) devices; cgo builds only
│   ├── ring.go            # lock-free ring buffer the audio callback writes into
│   ├── level.go           # RMS / peak levels for the overlay
│   ├── devices.go         # input / output device enumeration
│   ├── feedback.go        # start / stop sounds
│   └── wav.go             # WAV read / write for history and the CLI
├── engine/
│   ├── engine.go          # Engine / Stream interfaces, Result, Word, options
│   ├── nemo/              # CGo binding to nemo_speech/asr.h (built with cgo only)
│   ├── worker/            # supervisor (parent) + worker (child) and their protocol
│   └── fake/              # scripted engine for tests and CGo-free builds
├── models/
│   ├── catalog.json       # the two models (+ Silero VAD), pinned revisions and checksums
│   ├── catalog.go
│   ├── download.go        # resumable download and verification of one file
│   ├── hfcache.go         # finds files already in the Hugging Face cache
│   └── manager.go         # list, download, delete; status per model and quantisation
├── pipeline/
│   └── coordinator.go     # recording → transcription → output state machine
├── text/                  # cleanup, filler words, custom-word correction, hook
├── llm/                   # optional post-processing
├── output/                # clipboard paste with restore, direct typing
├── hotkey/                # global shortcuts, push-to-talk and toggle
├── history/               # SQLite history + saved recordings
├── platform/              # permissions and native window tweaks (per-OS, CGo)
└── ui/
    ├── tray.go
    ├── overlay.go
    ├── onboarding.go
    └── settings/          # one file per settings page
packaging/
├── macos/Info.plist       # bundle metadata (LSUIElement: menu bar app, no Dock icon)
└── huggingface/           # model cards of the GGUF repositories Goscriber publishes
scripts/
├── build-nemo-speech.sh   # builds and installs the pinned NeMo-Speech.cpp SDK into .deps/
├── convert-models.sh      # reproduces (and publishes) the Parakeet TDT v2 and Silero VAD GGUFs
└── wer.py                 # scores model evaluations (docs/models/)
third_party/
└── nemo-speech/           # NeMo-Speech.cpp v0.3.0 (git submodule)
```

## App shell

`goscriber` with no subcommand starts the Fyne app (`io.github.opragmatico.goscriber`) as a menu bar / tray icon:

- **Tray menu**: Start / Stop Dictation, Cancel, the current model (or "No model: Download…", which opens the Models page, with the dictation items disabled), Settings… and Quit. With debug mode on, a Debug submenu forces the icon state.
- **Icon per state**: idle and transcribing are template icons that follow the menu bar's colour; recording is red. The coordinator drives the state.
- **No window at launch** while the tray is shown; windows open from the menu. Without a tray (`--no-tray`, the `show_tray_icon` setting, or a desktop with no tray) the settings window opens at launch, even with `--start-hidden`, and closing it quits, as in Handy.
- **macOS**: with a tray the app is an accessory app with no Dock icon. The bundle's Info.plist sets `LSUIElement`, and the app also sets the activation policy at startup, because GLFW makes every app a regular one and plain `go run` builds have no Info.plist. Without a tray the Dock icon stays as the way back in.
- **Shutdown**: Quit, SIGINT and SIGTERM all end Fyne's main loop; the app then stops following settings and closes the log.

## Shortcuts

`internal/hotkey` registers two global shortcuts from the settings: **transcribe** (`option+space` on macOS, `ctrl+space` on Windows, `ctrl+alt+space` on Linux) and **cancel** (`escape`). A shortcut is modifiers and one key, written as settings store it (`ctrl+shift+d`, `cmd+f5`); a plain letter, digit or Space is refused because a global shortcut swallows its key in every app, and only the cancel shortcut may be a plain Escape. The cancel shortcut is registered only while dictation is recording, so Escape works normally the rest of the time.

Key-downs and key-ups of the transcribe shortcut drive the activation mode: **toggle** (press to start, press to stop), **push-to-talk** (hold to record) and **hold-or-toggle**, the default (a press starts recording; released after `hold_threshold_ms`, 300 ms, it was push-to-talk and stops, released sooner it was a tap and recording goes on until the next press). The manager owns the registrations on one goroutine and re-registers when the settings change, without a restart; a shortcut that cannot be registered is logged and handed to `OnError`, and the next settings change retries it.

On macOS, `golang.design/x/hotkey` hears shortcuts through a keyboard event tap, which consumes the key and needs the **Accessibility** permission (the same one pasting needs). Registration dispatches onto the main thread, which Fyne's loop services; the manager never makes the main thread wait for it. An event tap hears no key presses while any process holds **Secure Event Input** (a focused password field, Terminal's Secure Keyboard Entry), so the app polls `IsSecureEventInputEnabled()` and logs a warning naming the holder when it stays on for 3 s, as Handy does.

**Modifier-only shortcuts** (the right Option key, Fn) are not supported: the library matches a key with modifiers, not a modifier on its own, and the parser refuses them with a clear message. Supporting them needs our own CGEventTap watching `flagsChanged` in `internal/platform`; since the library's tap already needs Accessibility, that adds no new permission. Handy also shadows keyed shortcuts with Carbon hotkeys while Secure Event Input is on, which Carbon is immune to; that is a possible follow-up.

## The CGo engine

NeMo-Speech.cpp exposes a stable C ABI: opaque handles, size-prefixed POD config structs, explicit ownership and status codes. `internal/engine/nemo` wraps it:

- **Recognizer**: `nemo_speech_asr_create` / `_destroy`. One recognizer per loaded model. Backend `gpu = 0` uses Metal (or CUDA / Vulkan where built), `gpu = -1` forces the CPU.
- **Offline**: `nemo_speech_asr_recognize_f32` for a whole utterance.
- **Streaming**: `nemo_speech_asr_streaming_recognize`, then `_stream_push_f32` / `_stream_next` while recording, `_stream_finish` and a final drain on release, `_stream_close`. With `interim_results` the stream yields partial hypotheses for the overlay and finals for output.
- **Options**: `language_code` (Nemotron locale or `auto`), word time offsets, and `speech_contexts` for custom-vocabulary boosting.

Rules the binding follows:

- Every config struct is zeroed and its `size` set to `C.sizeof_…`; `*_default()` helpers are used where they exist.
- `nemo_speech_asr_last_error()` is thread-local, so each fallible call is made through a small C shim that copies the error message in the same C call. Go may move a goroutine between OS threads between two CGo calls.
- Result strings are copied with `C.GoString` before `nemo_speech_asr_result_destroy`.
- Audio is passed as a pointer to a `[]float32` for the duration of the call only.
- Handles live in Go structs with explicit `Close()` methods. Finalizers are only a safety net.
- A stream is driven by one goroutine at a time. The recognizer may be shared.

The binding needs `cgo` and the NeMo-Speech.cpp SDK (headers and shared libraries), which `make deps` builds into `.deps/nemo-speech` and Go finds through pkg-config. [The SDK guide](engine-sdk.md) covers the build, why the libraries are shared rather than static, and what that means for packaging. Packages that do not need the engine build and test without CGo by using `engine/fake`.

### Process isolation

A `GGML_ASSERT`, a GPU driver fault or an `abort()` inside C++ takes the whole Go process with it. Following Handy's `engine_supervisor`, the native engine runs in a child process: the same `goscriber` binary started with a hidden `engine-worker` subcommand. The parent's `engine/worker` supervisor implements the same `Engine` interface over a framed stdin/stdout protocol (load, unload, recognize, stream push / next / finish, cancel), restarts a crashed worker, and retries on the CPU when the GPU backend was at fault. A worker lives exactly as long as its loaded model, so unloading returns all native memory to the OS.

## Dictation pipeline

```text
shortcut down ─► Capture.Start()
                 │  float32 frames ─► level meter ─► overlay
                 │                └► [streaming] Stream.Push ─► Next ─► interim text ─► overlay
shortcut up   ─► Capture.Stop() ─► speech gate (too short / too quiet ─► no speech)
                 └► [streaming] Stream.Finish ─► drain finals
                    [batch]     Recognize(whole clip, or the last chunk when chunking)
                    └► empty transcript ─► no speech
                    └► text cleanup ─► [LLM post-processing] ─► [hook] ─► paste / type ─► history
```

**No speech** is an outcome, not an error: nothing is pasted and the user sees no notice, only a debug log line. Three checks lead to it (`internal/pipeline/speech.go`): a recording shorter than `min_recording_ms` (250 ms), one whose loudest 33 ms window is under `silence_threshold_db` (−60 dBFS) — both skip the model — and an empty or whitespace-only final transcript. The Silero VAD masks silence on the streaming path only; NeMo-Speech.cpp v0.3.0 does not apply it to whole-clip decodes ([details](engine-sdk.md#voice-activity-detection)).

The `Coordinator` owns a single state machine: `idle → recording → transcribing → post_processing → outputting → idle`. A cancel from any state returns to `idle` and discards the session. Only one session is active at a time.

## Threading model

- **Fyne main thread**: all widgets, tray, overlay. Other goroutines update the UI only through `fyne.Do` / `fyne.DoAndWait`.
- **Audio callback (miniaudio thread)**: copies frames into a ring buffer and returns; never blocks or calls into the engine.
- **Hotkey goroutine**: owns the shortcut registrations and turns key-down / key-up events into coordinator actions, which reach the coordinator through `fyne.Do`.
- **Engine goroutine**: one goroutine owns the engine client and serialises load, unload and recognition; a streaming session pumps audio from the ring buffer to `Push` / `Next`.
- **Background goroutines**: model downloads, history I/O, LLM requests.

Goroutines talk through channels and the coordinator; nothing outside the main thread touches a widget directly.

## Data locations

- **Settings**: `os.UserConfigDir()/goscriber/settings.json`
- **Models**: `os.UserCacheDir()/goscriber/models/` (GGUF files under their Hugging Face names, `*.part` while downloading, and `verified.json`, which remembers the files already hashed). Files already in the Hugging Face cache (`HF_HUB_CACHE`, `HF_HOME`, default `~/.cache/huggingface/hub`) are used from there.
- **History**: `os.UserConfigDir()/goscriber/history.sqlite3` and `…/recordings/*.wav`
- **Logs**: `os.UserCacheDir()/goscriber/logs/goscriber.log`
