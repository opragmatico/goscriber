# Architecture

Goscriber is a Go re-implementation of [Handy](https://github.com/cjpais/Handy): press a shortcut, speak, and the transcription is pasted into whatever app has focus, fully offline.

Handy is built with Tauri (Rust backend + React UI) and runs a broad catalog of GGUF models through `transcribe.cpp`. Goscriber keeps the same product behaviour, swaps the stack for Go + Fyne, and narrows speech recognition to three NVIDIA models served by [NeMo-Speech.cpp](https://github.com/NVIDIA/NeMo-Speech.cpp) through CGo.

| Concern | Handy | Goscriber |
| --- | --- | --- |
| UI | Tauri webview + React | Fyne v2 widgets |
| Tray / overlay | Tauri tray, NSPanel / webview overlay | Fyne `desktop.App` tray menu; borderless Fyne window, created without focus and made floating and click-through through native window handles |
| Speech-to-text | `transcribe-cpp` (many families) | NeMo-Speech.cpp C ABI (`nemo_speech/asr.h`) via CGo, three models only |
| Engine isolation | `engine_supervisor`: worker child process | Same idea: the `goscriber` binary re-launched as an engine worker |
| Model download | Catalog + downloader with mirrors | Embedded three-model catalog, resumable HTTP download, SHA-256 verification |
| Voice activity detection | Silero VAD v6 (GGUF) | Silero VAD GGUF loaded by NeMo-Speech.cpp (feature masking, endpointing) |
| Audio capture | `handy-recorder` (cpal) | `malgo` (miniaudio); NeMo-Speech.cpp resamples 8–96 kHz input itself |
| Global shortcuts | `handy-keys` (CGEventTap) / `rdev` | `golang.design/x/hotkey` (a CGEventTap on macOS, XGrabKey on X11) |
| Paste / typing | `enigo`, platform paste code | `NSPasteboard` and `CGEventPost` through CGo on macOS |
| Settings | `tauri-plugin-store` (JSON) | Go structs persisted as JSON under `os.UserConfigDir()` |
| History | SQLite (`rusqlite`) | SQLite (`modernc.org/sqlite`, pure Go) + WAV files |
| LLM post-processing | OpenAI-compatible HTTP clients | `net/http` OpenAI-compatible client |
| Single instance / CLI | `tauri-plugin-single-instance`, `clap` | Unix socket / named pipe, stdlib `flag` |
| Sound effects | `rodio` | `malgo` playback |

## Models

| | Nemotron Streaming 3.5 | Parakeet TDT 0.6B v2 | Parakeet TDT 0.6B v3 |
| --- | --- | --- | --- |
| Source | [`nvidia/nemotron-3.5-asr-streaming-0.6b`](https://huggingface.co/nvidia/nemotron-3.5-asr-streaming-0.6b) | [`nvidia/parakeet-tdt-0.6b-v2`](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v2) | [`nvidia/parakeet-tdt-0.6b-v3`](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3) |
| Architecture | Cache-aware FastConformer-RNNT with language-ID prompts | FastConformer-TDT (token-and-duration transducer), full attention | As v2, with an 8,192-token multilingual vocabulary |
| Languages | 40+ locales, or `auto` | English, with punctuation and capitalisation | 25 European languages, with punctuation and capitalisation. The model detects the language; a chosen language is accepted but not enforced, and the detected one is not reported ([checks](models/parakeet-tdt-0.6b-v3.md)) |
| Decoding | Streaming with interim results | Offline only (NeMo-Speech.cpp rejects streaming for TDT); decoded in chunks of up to about 30 s split at pauses, because some longer inputs come back with stretches missing ([spike](models/parakeet-tdt-0.6b-v2.md#long-audio)) | As v2 |
| GGUF | Published by NVIDIA (`*.q8_0.gguf`, ~742 MB) | Converted by us with NeMo-Speech.cpp's `convert_model.py`, then hosted with a pinned revision | Published by NVIDIA (`*.q8_0.gguf`, ~714 MB), without the embedded tokenizer word boosting needs |
| License | OpenMDW 1.1 (at the pinned revision) | CC-BY-4.0 | CC-BY-4.0 |
| Live text | Interim results in the overlay | Optional chunked decode at pauses while recording | As v2 |

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
├── notice/                # one place to report background errors: hub, de-duplication, fan-out
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
│   ├── autounload/        # unloads an idle model, reloads it on next use
│   └── fake/              # scripted engine for tests and CGo-free builds
├── models/
│   ├── catalog.json       # the three models (+ Silero VAD), pinned revisions and checksums
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
├── platform/              # permissions, the overlay's native window tweaks (per-OS, CGo)
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
- **Icon per state**: idle and transcribing are template icons that follow the menu bar's colour; recording is red. The coordinator drives the state; post-processing and outputting show as transcribing.
- **No window at launch** while the tray is shown; windows open from the menu. Without a tray (`--no-tray`, the `show_tray_icon` setting, or a desktop with no tray) the settings window opens at launch, even with `--start-hidden`, and closing it quits, as in Handy.
- **macOS**: with a tray the app is an accessory app with no Dock icon. The bundle's Info.plist sets `LSUIElement`, and the app also sets the activation policy at startup, because GLFW makes every app a regular one and plain `go run` builds have no Info.plist. Without a tray the Dock icon stays as the way back in.
- **Shutdown**: Quit, SIGINT and SIGTERM all end Fyne's main loop; the app then stops following settings and closes the log.

## Overlay

While a session is in progress, a small pill near the edge of the screen shows that Goscriber is listening (`internal/ui/overlay.go`): a red dot, a level meter and the elapsed time while recording, "Transcribing…" with an amber dot after it, and a cancel button in both. `overlay_style` picks how much: `none` (never shown; the default on Linux, where overlays steal focus on several desktops, as in Handy), `minimal` (the pill) or `live` (the default elsewhere: the pill widens to show the end of the interim text, which live transcription, #24, fills in). `overlay_position` puts it at the top or the bottom centre of the screen that holds the mouse pointer.

The window is a borderless Fyne window (`desktop.Driver.CreateSplashWindow`, asked to stay on top), created the first time there is something to show. It must never take the keyboard from the app the user dictates into, or the paste would land in Goscriber:

- **No focus on show, on every OS**: GLFW focuses a window when it shows it unless the window was created with `GLFW_FOCUS_ON_SHOW` off. Fyne never sets that hint, so the overlay sets it (through `go-gl/glfw`, which Fyne's driver owns) only around the window's creation, then puts it back for windows created later.
- **macOS** (`platform.PrepareOverlay`, through `driver.NativeWindow.RunNative`): the `NSWindow` gets the status window level, joins all Spaces and full-screen apps (`CanJoinAllSpaces`, `FullScreenAuxiliary`, `Stationary`, `IgnoresCycle`), loses its shadow, and has its content clipped to a pill by its layer. AppKit can only pass a window's clicks through as a whole, so the overlay ignores the mouse except while the pointer is over the cancel button, which it checks ten times a second while shown (`platform.OverlayClickThrough`). Clicking cancel makes Goscriber the active app, so the overlay then hands the keyboard back (`[NSApp deactivate]`). It is placed with `NSScreen.visibleFrame`, clear of the menu bar and the Dock.
- **Elsewhere** the window is centred on the screen and Fyne's always-on-top request applies; Linux and Windows positioning come with #35 and #36.

The shell feeds it from the coordinator's events on the main thread: the state, the interim text, and the microphone level at most about 40 times a second (the recorder measures 30), with at most one level update and one clock tick waiting for the main thread at a time. If adjusting the GLFW window turns out not to be enough (it may still activate the app on some macOS versions), the fallback the issue foresaw is to draw this one overlay as a native `NSPanel` through CGo.

## Shortcuts

`internal/hotkey` registers two global shortcuts from the settings: **transcribe** (`option+space` on macOS, `ctrl+space` on Windows, `ctrl+alt+space` on Linux) and **cancel** (`escape`). A shortcut is modifiers and one key, written as settings store it (`ctrl+shift+d`, `cmd+f5`); a plain letter, digit or Space is refused because a global shortcut swallows its key in every app, and only the cancel shortcut may be a plain Escape. The cancel shortcut is registered only while dictation is recording, so Escape works normally the rest of the time.

Key-downs and key-ups of the transcribe shortcut drive the activation mode: **toggle** (press to start, press to stop), **push-to-talk** (hold to record) and **hold-or-toggle**, the default (a press starts recording; released after `hold_threshold_ms`, 300 ms, it was push-to-talk and stops, released sooner it was a tap and recording goes on until the next press). The manager owns the registrations on one goroutine and re-registers when the settings change, without a restart; a shortcut that cannot be registered is logged and handed to `OnError`, and the next settings change retries it.

On macOS, `golang.design/x/hotkey` hears shortcuts through a keyboard event tap, which consumes the key and needs the **Accessibility** permission (the same one pasting needs). Registration dispatches onto the main thread, which Fyne's loop services; the manager never makes the main thread wait for it. An event tap hears no key presses while any process holds **Secure Event Input** (a focused password field, Terminal's Secure Keyboard Entry), so the app polls `IsSecureEventInputEnabled()` and logs a warning naming the holder when it stays on for 3 s, as Handy does.

**Modifier-only shortcuts** (the right Option key, Fn) are not supported: the library matches a key with modifiers, not a modifier on its own, and the parser refuses them with a clear message. Supporting them needs our own CGEventTap watching `flagsChanged` in `internal/platform`; since the library's tap already needs Accessibility, that adds no new permission. Handy also shadows keyed shortcuts with Carbon hotkeys while Secure Event Input is on, which Carbon is immune to; that is a possible follow-up.

## Notices

Background problems reach the user through one place, `internal/notice`. A `Notice` has a code, a level (error: nothing came out or Goscriber cannot work; warning: a result, but not the one set up), a source, a message ready to show, and optionally an action (a button that fixes it). Callers publish it to the app's `Hub`, which keeps one active notice per code:

- **De-duplication**: publishing a code that is already active updates its message and raises nothing, so unplugging the microphone mid-recording, which makes the recorder report several things in a row, shows one notice.
- **Persistent problems** (no model downloaded, a missing permission) stay active until resolved: the model gets selected, the permission is granted, the shortcut registers. **Transient ones** (a failed paste, an engine crash) also end on their own after two minutes without a repeat, so the same failure much later is news again.
- **Fan-out never blocks**: each subscriber has its own queue.

The app shell routes each new notice once: an OS notification (`fyne.App.SendNotification`) when no window is open, otherwise a banner across the top of the settings window, which also shows the active notice whenever the window opens, with the action and a Dismiss button. Persistent problems also turn the idle tray icon amber, and a missing permission adds a menu item that opens System Settings at Accessibility. While that permission is missing the shell checks every two seconds whether it was granted, then clears the notice and registers the shortcut again.

| Code | Level | Persistent | Action | Raised by |
| --- | --- | --- | --- | --- |
| `no_model` | error | yes | Download a model → Models page | the shell, at launch and when the selected model goes away; the coordinator, raised afresh on every Start without a model |
| `permission_missing` | error | yes | Open System Settings | a shortcut or paste refused for lack of Accessibility |
| `shortcut_failed` | error | no | Open Settings | a shortcut that cannot register for another reason |
| `paste_failed` | error | no | | a failed delivery (`notice.ForPasteError`) |
| `microphone_unavailable` | error | no | | the recorder's notifications, and a microphone that will not open at Start |
| `model_corrupt` | error | no | Download again → Models page | a model that fails to load or verify |
| `engine_crashed` | error | no | | the engine worker (#28) |
| `engine_cpu_fallback` | warning | no | | the engine worker (#28) |
| `transcription_failed` | error | no | | the coordinator, when a decode fails or a model will not load for a reason other than a damaged file |

`engine_crashed` and `engine_cpu_fallback` have constructors and tests but no producer until the engine worker (#28) exists.

## Output

`internal/output` puts the final transcript into the focused app, in one of three ways (`paste_method`):

- **paste** (default): save the clipboard, write the transcript, wait `paste_delay_ms` (60 ms), send Cmd+V (Ctrl+V elsewhere), wait `paste_delay_after_ms` (60 ms) so the app reads it, then put the clipboard back. With `clipboard_handling: keep` the transcript stays on the clipboard instead. The clipboard is restored even when the keystroke fails, and not at all if something else wrote to it in the meantime (the user copied during the paste), so a newer copy is never overwritten.
- **direct**: type the transcript; the clipboard is not touched.
- **none**: copy the transcript to the clipboard and leave it there.

`append_trailing_space` adds a space after the transcript and `auto_submit` presses Return after it (paste and direct only). Deliveries are serialised, so two pastes never restore each other's transcript as the user's clipboard. `goscriber debug paste` runs a delivery from the command line, after a few seconds to switch apps.

On macOS the clipboard is `NSPasteboard` itself rather than `golang.design/x/clipboard`, which only reads text and PNG: saving copies every item in every type it offers (rich text, images, file URLs, app-private types) into Go memory, and restoring writes them all back, or leaves the clipboard empty if it was. The transcript is marked with the [nspasteboard.org](http://nspasteboard.org) transient and auto-generated types while it is there only to be pasted, so clipboard managers leave it out of their history. Key presses go through `CGEventPost` from a private event source with explicit flags, so the Option still held from Option+Space does not turn Cmd+V into another shortcut. The paste key is the one that types "v" with Command held in the active keyboard layout (`UCKeyTranslate`, read on the main thread), so Cmd+V works on Dvorak; direct typing attaches the text to key events with `CGEventKeyboardSetUnicodeString`, 20 UTF-16 units at a time. Posting needs the **Accessibility** permission: without it macOS drops the events silently, so the keyboard checks `AXIsProcessTrusted()` first and fails with `output.ErrNoPermission`. Linux and Windows are issues #35 and #36.

Handy's "reliable paste" (restore only once the target app has read the pasteboard) is a later improvement; Goscriber starts with the delays.

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

### Idle unload

A loaded model holds hundreds of megabytes, so `engine/autounload` wraps the engine (the supervisor, or the binding directly) and unloads the model once it has been idle for `model_unload_timeout`: a number of seconds (30 by default; `0` unloads it as soon as it is idle), or `"never"`. The model is busy while a `Recognize` is in flight, a stream is open, or the coordinator holds it with `Acquire`, which it does from the start of a recording until the transcript is out. Holding it then hides a reload behind the user speaking, and a long batch recording, when the engine itself has no work, cannot outlast the timeout. An idle unload keeps the `LoadSpec`, and the next call loads the same model again; an explicit `Unload` forgets it. The coordinator passes settings changes on with `SetTimeout`.

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

The `Coordinator` (`internal/pipeline/coordinator.go`) owns a single state machine: `idle → recording → transcribing → post_processing → outputting → idle`. A cancel from any state returns to `idle` and discards the session. Only one session is active at a time.

- **Actions**: `Start`, `Stop`, `Toggle` and `Cancel` (also `Do(action)`), from the shortcut, the tray and later the command line (#33). They never block: they are queued and run in order on the coordinator's own goroutine, so they are safe on the UI thread. An action that does not fit the state is refused, not queued: the shortcut pressed while a transcript is on its way does nothing.
- **Events**: `Subscribe` delivers state changes, refusals, the microphone level while recording (coalesced, so a slow reader gets the latest), interim text (from #24) and the delivered transcript. A refusal carries the unchanged state, so the shortcut's activator, which guessed the press would start or stop a recording, resyncs. The shell follows the events to drive the tray icon and to register Escape only while recording.
- **Model**: every `Start` resolves the selected model through a `ModelSource` (`CatalogModels` over the models manager: `selected_model`, in `model_quant` when the model has it, on the CPU when `accelerator` says so, with the Silero VAD when enabled and downloaded). With no model ready, `Start` records nothing and raises `no_model` afresh, so every press is answered; a damaged file raises `model_corrupt`. The engine goroutine loads the model when recording starts, if the engine does not hold it already, and `Acquire`s it until the transcript is out.
- **Batch decode**: on `Stop`, the speech gate runs first (too short or too quiet ends the session quietly), then the whole clip is decoded with `Recognize` on the engine goroutine, behind the load. The streaming path comes with live transcription (#24).
- **Session IDs**: each session has an ID and a context. A cancel ends the context (the decode, a text stage or the paste may stop early) and anything that finishes afterwards for that session is dropped, even while the next session is recording.
- **Stages**: `Stages{Cleanup, LLM, Hook, History}` run after recognition, each optional and nil until its issue lands (#27, #39, #38, #32). A failing text stage passes its input on; empty output pastes nothing. History saves after delivery, off the critical path.
- **Failures**: an engine error ends in `idle` with `transcription_failed`, `model_corrupt` or `no_model`; a failed delivery with `paste_failed` or `permission_missing`. Each is raised before the state returns to idle. No speech is not a failure.
- **Timings**: each stage's duration is logged at debug level, and a summary line per session (`dictation timings`); the info-level line says only whether something was delivered and how many characters, never the text.

Until onboarding (#31) and the Models page (#30) exist, choose the model by setting `selected_model` in `settings.json` to a downloaded model's id, for example `"nemotron-3.5-asr-streaming-0.6b"` after `goscriber models pull nemotron-3.5-asr-streaming-0.6b`.

## Threading model

- **Fyne main thread**: all widgets, tray, overlay. Other goroutines update the UI only through `fyne.Do` / `fyne.DoAndWait`.
- **Audio callback (miniaudio thread)**: copies frames into a ring buffer and returns; never blocks or calls into the engine.
- **Hotkey goroutine**: owns the shortcut registrations and turns key-down / key-up events into coordinator actions, which it hands straight to the coordinator: its actions never block.
- **Coordinator goroutine**: runs the actions in order (opening the microphone, stopping it, refusing what does not fit the state) and owns the session's state with a mutex.
- **Engine goroutine**: one goroutine owns the engine client and serialises load, acquire and recognition; a streaming session pumps audio from the ring buffer to `Push` / `Next` (#24).
- **Session goroutine**: per session, runs the text stages, the delivery and the history save, so a slow paste, LLM or hook never holds up a cancel.
- **Background goroutines**: model downloads, history I/O, LLM requests.

Goroutines talk through channels and the coordinator; nothing outside the main thread touches a widget directly.

## Data locations

- **Settings**: `os.UserConfigDir()/goscriber/settings.json`
- **Models**: `os.UserCacheDir()/goscriber/models/` (GGUF files under their Hugging Face names, `*.part` while downloading, and `verified.json`, which remembers the files already hashed). Files already in the Hugging Face cache (`HF_HUB_CACHE`, `HF_HOME`, default `~/.cache/huggingface/hub`) are used from there.
- **History**: `os.UserConfigDir()/goscriber/history.sqlite3` and `…/recordings/*.wav`
- **Logs**: `os.UserCacheDir()/goscriber/logs/goscriber.log`
