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
| Decoding | Streaming with interim results | Offline only (NeMo-Speech.cpp rejects streaming for TDT); decoded in chunks of 3 to 25 s cut at pauses, because some longer inputs come back with stretches missing ([spike](models/parakeet-tdt-0.6b-v2.md#long-audio)) | As v2 |
| GGUF | Published by NVIDIA (`*.q8_0.gguf`, ~742 MB) | Converted by us with NeMo-Speech.cpp's `convert_model.py`, then hosted with a pinned revision | Published by NVIDIA (`*.q8_0.gguf`, ~714 MB), without the embedded tokenizer word boosting needs |
| License | OpenMDW 1.1 (at the pinned revision) | CC-BY-4.0 | CC-BY-4.0 |
| Live text | Interim results in the overlay, a word within about 0.2 s of being spoken | Each chunk in the overlay once it is decoded, while recording (`long_form_chunking`, on by default) | As v2 |

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
│   ├── feedback.go        # start / stop cues, mute while recording
│   ├── sounds/            # the cues, made by gen_sounds.go
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

While a session is in progress, a small pill near the edge of the screen shows that Goscriber is listening (`internal/ui/overlay.go`): a red dot, a level meter and the elapsed time while recording, "Transcribing…" with an amber dot after it, and a cancel button in both. `overlay_style` picks how much: `none` (never shown; the default on Linux, where overlays steal focus on several desktops, as in Handy), `minimal` (the pill) or `live` (the default elsewhere: the pill widens to show the end of the transcript so far, from [live transcription](#live-transcription)). The words that are settled are drawn in full ink and the ones that may still change dimmed. `overlay_position` puts it at the top or the bottom centre of the screen that holds the mouse pointer.

The window is a borderless Fyne window (`desktop.Driver.CreateSplashWindow`, asked to stay on top), created the first time there is something to show. It must never take the keyboard from the app the user dictates into, or the paste would land in Goscriber:

- **No focus on show, on every OS**: GLFW focuses a window when it shows it unless the window was created with `GLFW_FOCUS_ON_SHOW` off. Fyne never sets that hint, so the overlay sets it (through `go-gl/glfw`, which Fyne's driver owns) only around the window's creation, then puts it back for windows created later.
- **macOS** (`platform.PrepareOverlay`, through `driver.NativeWindow.RunNative`): the `NSWindow` gets the status window level, joins all Spaces and full-screen apps (`CanJoinAllSpaces`, `FullScreenAuxiliary`, `Stationary`, `IgnoresCycle`), loses its shadow, and has its content clipped to a pill by its layer. AppKit can only pass a window's clicks through as a whole, so the overlay ignores the mouse except while the pointer is over the cancel button, which it checks ten times a second while shown (`platform.OverlayClickThrough`). Clicking cancel makes Goscriber the active app, so the overlay then hands the keyboard back (`[NSApp deactivate]`). It is placed with `NSScreen.visibleFrame`, clear of the menu bar and the Dock.
- **Elsewhere** the window is centred on the screen and Fyne's always-on-top request applies; Linux and Windows positioning come with #35 and #36.

The shell feeds it from the coordinator's events on the main thread: the state, the interim text at most ten times a second (always ending on the newest, and never a session's text after it ended), and the microphone level at most about 40 times a second (the recorder measures 30), with at most one level update, one interim and one clock tick waiting for the main thread at a time. If adjusting the GLFW window turns out not to be enough (it may still activate the app on some macOS versions), the fallback the issue foresaw is to draw this one overlay as a native `NSPanel` through CGo.

## Sounds

With `audio_feedback` on (off by default, as in Handy), a short rising chime plays when recording starts and a falling one when it stops (`internal/audio/feedback.go`). Both were made for Goscriber by `internal/audio/gen_sounds.go` (`go generate ./internal/audio`) and are embedded as WAV files, so they carry no third-party licence. They play through miniaudio on `output_device` (a device that is not connected falls back to the default) at `audio_feedback_volume`.

The start cue plays to its end, plus the device's buffers, before the microphone starts, so it is not transcribed; an always-on microphone's pre-roll, which heard it, is dropped (`Recorder.DropPreRoll`). The stop cue plays once the microphone has stopped, without holding up the transcription. A cancel plays nothing.

With `mute_while_recording` on, the system's default output is muted through CoreAudio after the start cue and put back as it was when the recording stops, is cancelled, fails to start or the app quits; a device that was already muted stays muted, and the device muted is the one restored even if the default changes meanwhile. Outputs without a mute switch (some HDMI and Bluetooth devices) are left alone with a warning. Muting is macOS-only for now. `goscriber debug sounds [--device D] [--volume V]` plays both cues.

## Permissions

On macOS Goscriber needs the **Microphone** permission to record and **Accessibility** for the shortcut's event tap and for posting the paste; **Input Monitoring** is not needed, because the shortcut library's tap is an active one, which Accessibility covers. At launch it asks once for each that is missing (macOS's own microphone prompt; the Accessibility dialog, remembered in Fyne's preferences so it is not shown at every launch), raises a notice with a button to System Settings for each that is refused, and shows them all on the settings window's Permissions page. Start refuses to record without the microphone permission, since macOS would deliver silence. `internal/platform` has the status API (`StatusOf`, `Request`, `PermissionStates`) for onboarding (#31). Permissions attach to the app's code signature, which matters during development: [permissions.md](permissions.md).

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

The app shell routes each new notice once: an OS notification (`fyne.App.SendNotification`) when no window is open, otherwise a banner across the top of the settings window, which also shows the active notice whenever the window opens, with the action and a Dismiss button. Persistent problems also turn the idle tray icon amber, and a missing permission adds a menu item that opens System Settings at its pane. While a permission is missing the shell checks every two seconds whether it was granted, then clears the notice (and, for Accessibility, registers the shortcut again). See [permissions.md](permissions.md).

| Code | Level | Persistent | Action | Raised by |
| --- | --- | --- | --- | --- |
| `no_model` | error | yes | Download a model → Models page | the shell, at launch and when the selected model goes away; the coordinator, raised afresh on every Start without a model |
| `permission_missing` | error | yes | Open System Settings | a shortcut or paste refused for lack of Accessibility, and the launch check |
| `microphone_denied` | error | yes | Open System Settings | the launch check, and a Start refused for lack of the microphone permission |
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
                 │                └► [chunking]  cut at pauses ─► Recognize(chunk) ─► chunks so far ─► overlay
shortcut up   ─► Capture.Stop() ─► speech gate (too short / too quiet ─► no speech)
                 └► [streaming] Stream.Finish ─► drain finals ─► join
                    [chunking]  Recognize(the open chunk) ─► join with the chunks decoded
                    [whole]     Recognize(whole clip)
                    (a live decode that failed falls back to Recognize of what it missed)
                    └► empty transcript ─► no speech
                    └► text cleanup ─► [LLM post-processing] ─► [hook] ─► paste / type ─► history
```

**No speech** is an outcome, not an error: nothing is pasted and the user sees no notice, only a debug log line. Three checks lead to it (`internal/pipeline/speech.go`): a recording shorter than `min_recording_ms` (250 ms), one whose loudest 33 ms window is under `silence_threshold_db` (−60 dBFS) — both skip the model — and an empty or whitespace-only final transcript. The Silero VAD masks silence on the streaming path only; NeMo-Speech.cpp v0.3.0 does not apply it to whole-clip decodes ([details](engine-sdk.md#voice-activity-detection)).

The `Coordinator` (`internal/pipeline/coordinator.go`) owns a single state machine: `idle → recording → transcribing → post_processing → outputting → idle`. A cancel from any state returns to `idle` and discards the session. Only one session is active at a time.

- **Actions**: `Start`, `Stop`, `Toggle` and `Cancel` (also `Do(action)`), from the shortcut, the tray and later the command line (#33). They never block: they are queued and run in order on the coordinator's own goroutine, so they are safe on the UI thread. An action that does not fit the state is refused, not queued: the shortcut pressed while a transcript is on its way does nothing.
- **Events**: `Subscribe` delivers state changes, refusals, the microphone level while recording and the transcript so far (both coalesced, so a slow reader gets the latest), and the delivered transcript. A refusal carries the unchanged state, so the shortcut's activator, which guessed the press would start or stop a recording, resyncs. The shell follows the events to drive the tray icon and to register Escape only while recording.
- **Model**: every `Start` resolves the selected model through a `ModelSource` (`CatalogModels` over the models manager: `selected_model`, in `model_quant` when the model has it, on the CPU when `accelerator` says so, with the Silero VAD when enabled and downloaded). With no model ready, `Start` records nothing and raises `no_model` afresh, so every press is answered; a damaged file raises `model_corrupt`. The engine goroutine loads the model when recording starts, if the engine does not hold it already, and `Acquire`s it until the transcript is out.
- **Decode**: the recording is decoded while it goes on ([live transcription](#live-transcription)). On `Stop`, the speech gate runs first (too short or too quiet ends the session quietly), then the engine goroutine finishes the decode: the stream's final, or the chunk still open, or the whole clip when neither applies.
- **Session IDs**: each session has an ID and a context. A cancel ends the context (the decode, a text stage or the paste may stop early) and anything that finishes afterwards for that session is dropped, even while the next session is recording.
- **Stages**: `Stages{Cleanup, LLM, Hook, History}` run after recognition, each optional and nil until its issue lands (#27, #39, #38, #32). A failing text stage passes its input on; empty output pastes nothing. History saves after delivery, off the critical path.
- **Failures**: an engine error ends in `idle` with `transcription_failed`, `model_corrupt` or `no_model`; a failed delivery with `paste_failed` or `permission_missing`. Each is raised before the state returns to idle. No speech is not a failure.
- **Timings**: each stage's duration is logged at debug level, and a summary line per session (`dictation timings`); the info-level line says only whether something was delivered and how many characters, never the text.

Until onboarding (#31) and the Models page (#30) exist, choose the model by setting `selected_model` in `settings.json` to a downloaded model's id, for example `"nemotron-3.5-asr-streaming-0.6b"` after `goscriber models pull nemotron-3.5-asr-streaming-0.6b`.

### Live transcription

From the moment recording starts, the coordinator hands the recorder's 10 ms frames (`Recorder.Frames`) to a pump on the engine goroutine, queued behind the model load, so audio that arrives while the model loads waits in the recorder and nothing is lost. What the pump does depends on the model the load reported (`internal/pipeline/live.go`):

- **Streaming model** (Nemotron Streaming 3.5): it opens a `Stream` with interim results, pushes each frame and pulls every result `Next` has. Each interim is published as `EventInterim` with the whole transcript so far; its `Stable` bytes are the endpointed utterances plus the words the previous interim already had, the rest is the end that may still change. On `Stop` it calls `Finish` and drains the end-of-stream final; the finals are joined (`engine.JoinFinals`). Interims only ever add words in practice: over the fixture no hypothesis rewrote a word the one before it had.
- **Offline model** (Parakeet TDT) with `long_form_chunking` (on by default): it cuts the recording into chunks at pauses as it arrives and decodes each finished chunk with `Recognize` while the user goes on speaking; the chunks decoded so far are the interim text, all of it stable. On `Stop` only the open chunk is left to decode, and the results are joined in order with their word times moved to their place in the recording. Chunks too quiet to hold speech (`silence_threshold_db`) are skipped.
- **Offline model, chunking off**: nothing happens until `Stop`, which decodes the whole clip.

**Chunks** (`internal/pipeline/chunker.go`). NeMo-Speech.cpp runs its VAD only inside the recognizer, so pauses are found by loudness: per 20 ms window, a window is quiet below a threshold 35 % of the way (in dB) from the noise floor to the speech level, the 10th and 90th percentiles of the last 10 s, and never under −60 dBFS. A chunk ends in the middle of a pause, so no word is cut or decoded twice. It is at least 3 s long; at 3 s a 700 ms pause ends it, and the pause needed falls to 250 ms by 15 s, so short chunks end where sentences do. A chunk that reaches 25 s without one ends at the quietest 100 ms of its last 10 s. Parakeet decodes 25 s in about 140 ms on an M4 Max with Metal, and no chunk comes near the 90 s inputs that came back empty in its checks.

**Failures.** If the stream fails to start, push, decode or finish, it is closed and `Stop` decodes the whole recording with `Recognize`. If a chunk fails, nothing more is decoded live and `Stop` decodes everything from that chunk on in one piece. Either way the user gets a transcript and a warning goes to the log; only a failure of that fallback raises `transcription_failed`.

**Cancel** ends the session's context: the pump stops decoding and drains the frames until the recorder closes them, a chunk being decoded is discarded, and the stream is closed without being finished.

**Latency** (`streaming_latency`: `low`, `balanced`, the default, or `accurate`) is how far a streaming model looks ahead before deciding on a word: NeMo-Speech.cpp's `rnnt_right_context`. It is part of the model's load spec, so a change reloads the model at the next recording; offline models leave it out. Measured on an M4 Max with Metal over `testdata/audio/jfk.wav` streamed in 20 ms chunks, from the end of a word (where the offline decode puts it) to the first interim holding it, in audio time:

| Preset | Right context | Interims over 11 s | Word shown after it ends: median / p90 / max | Finish → final |
| --- | --- | --- | --- | --- |
| `low` | 1 (160 ms chunks) | 63 | 20 / 100 / 180 ms | 17 ms |
| `balanced` | -1, the model's 3 (320 ms) | 31 | 100 / 180 / 180 ms | 22 ms |
| `accurate` | 13 (1.12 s) | 9 | 420 / 900 / 980 ms | 46 ms |

Each `Next` that decodes a chunk takes about 12 to 25 ms, so the overlay shows a word within about 0.2 s of its end at `low` and `balanced`, and within a second at `accurate`. Every preset transcribes the fixture without error; `accurate` is the one the model card's word error rates are measured with.

**Measured end to end** (`TestLiveNemotronDictation`, `TestLiveParakeetChunkedDictation` in `internal/engine/nemo`, the coordinator on the real engine with the recording played in 10 ms frames, M4 Max, Metal):

- Nemotron, `balanced`, the fixture in real time: words show in the overlay a median 125 ms and at most 209 ms after they end, and the transcript is ready 30 ms after the recording stops.
- Parakeet TDT 0.6B v2, a two-minute dictation (the fixture eleven times): decoded in 32 chunks while recording, ready 57 ms after the recording stops, word for word what was said. Decoding the same recording whole takes 1.2 s.

The output then waits `paste_delay_ms` (60 ms) before pressing the paste key.

**Long dictation.** A dictation is one stream with endpointing off, and that stays cheap however long it gets. Streaming ten minutes of speech, the process's footprint stays flat after the first stream allocates its compute buffers (about 300 MB, once per loaded model; about 1 MB more over the ten minutes), each `Next` stays at 14 to 27 ms, and `Finish` → final at about 20 ms. NeMo-Speech.cpp's cache-aware runner trims its audio and feature buffers as it goes and keeps a fixed-size encoder cache; only the transcript itself grows, by about 9 KB over ten minutes. Mid-stream endpointing (`endpointing.enable`, `stop_history_eou_ms`) would only split that transcript into finals: the runner keeps its caches across an end of utterance, so the memory is the same with it: forcing an endpoint after every sentence of the ten minutes gave the same footprint and timings. Goscriber therefore leaves endpointing off, and still joins finals in order in case a stream returns several.

## Threading model

- **Fyne main thread**: all widgets, tray, overlay. Other goroutines update the UI only through `fyne.Do` / `fyne.DoAndWait`.
- **Audio callback (miniaudio thread)**: copies frames into a ring buffer and returns; never blocks or calls into the engine.
- **Hotkey goroutine**: owns the shortcut registrations and turns key-down / key-up events into coordinator actions, which it hands straight to the coordinator: its actions never block.
- **Coordinator goroutine**: runs the actions in order (opening the microphone, stopping it, refusing what does not fit the state) and owns the session's state with a mutex.
- **Engine goroutine**: one goroutine owns the engine client and serialises load, acquire and recognition. While recording it pumps the recorder's frames into the live decode (`Push` / `Next`, or `Recognize` per chunk); on `Stop` it finishes the decode.
- **Session goroutine**: per session, runs the text stages, the delivery and the history save, so a slow paste, LLM or hook never holds up a cancel.
- **Background goroutines**: model downloads, history I/O, LLM requests.

Goroutines talk through channels and the coordinator; nothing outside the main thread touches a widget directly.

## Data locations

- **Settings**: `os.UserConfigDir()/goscriber/settings.json`
- **Models**: `os.UserCacheDir()/goscriber/models/` (GGUF files under their Hugging Face names, `*.part` while downloading, and `verified.json`, which remembers the files already hashed). Files already in the Hugging Face cache (`HF_HUB_CACHE`, `HF_HOME`, default `~/.cache/huggingface/hub`) are used from there.
- **History**: `os.UserConfigDir()/goscriber/history.sqlite3` and `…/recordings/*.wav`
- **Logs**: `os.UserCacheDir()/goscriber/logs/goscriber.log`
