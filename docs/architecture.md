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
| Global shortcuts | `handy-keys` (CGEventTap) / `rdev` | `golang.design/x/hotkey` (a CGEventTap on macOS, XGrabKey on X11); on Wayland, the XDG GlobalShortcuts portal over D-Bus (`godbus/dbus`), or the desktop's own shortcut sending `SIGUSR2`, as in Handy |
| Paste / typing | `enigo`, platform paste code, typing tools on Linux | `NSPasteboard` and `CGEventPost` through CGo on macOS; X11 selections and XTEST through CGo on Linux, or wtype, dotool or ydotool on Wayland ([linux.md](linux.md)) |
| Settings | `tauri-plugin-store` (JSON) | Go structs persisted as JSON under `os.UserConfigDir()` |
| History | SQLite (`rusqlite`) | SQLite (`modernc.org/sqlite`, pure Go) + WAV files |
| LLM post-processing | HTTP clients for OpenAI-compatible and Anthropic Messages APIs | `net/http` OpenAI-compatible client, planned ([llm-post-processing.md](llm-post-processing.md)) |
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
├── instance/              # single-instance lock and control socket
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
│   ├── decode.go          # WAV or FLAC files, told apart by content, for the History page and the CLI
│   └── wav.go             # WAV read / write for history and the CLI
├── engine/
│   ├── engine.go          # Engine / Stream interfaces, Result, Word, options
│   ├── nemo/              # CGo binding to nemo_speech/asr.h (built with cgo only)
│   ├── worker/            # supervisor (parent) + worker (child) and their protocol
│   ├── autounload/        # unloads an idle model, reloads it on next use
│   ├── warmup/            # decodes silence after each load, so no dictation runs cold
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
├── typing/                # Linux typing tools: xdotool, wtype, dotool, ydotool
├── session/               # X11 or Wayland
├── hotkey/                # global shortcuts, push-to-talk and toggle
│   └── portal/            # the XDG GlobalShortcuts portal, for Wayland (D-Bus)
├── history/               # SQLite history + saved recordings
├── platform/              # permissions, clipboard and key events, the overlay's native window tweaks, login items (per-OS, CGo)
│   └── x11test/           # another X11 app, for the Linux clipboard and key-event tests
└── ui/
    ├── tray.go
    ├── overlay.go
    ├── onboarding.go      # first-run onboarding, shown in the settings window
    ├── settings.go        # the settings window: sidebar, banner, debug shortcut
    ├── settings_bind.go   # widgets bound to settings fields, row layout
    ├── settings_*.go      # one file per page: general, models, advanced, history, debug, about…
    ├── shortcut_input.go  # records a key combination
    └── word_list.go       # edits custom and filler words
packaging/
├── macos/Info.plist       # bundle metadata (LSUIElement: menu bar app, no Dock icon)
├── linux/                 # the desktop entry of the Linux packages
└── huggingface/           # model cards of the GGUF repositories Goscriber publishes
scripts/
├── build-nemo-speech.sh   # builds and installs the pinned NeMo-Speech.cpp SDK into .deps/
├── package-linux.sh       # the Linux tarball and .deb (make dist-linux)
├── convert-models.sh      # reproduces (and publishes) the Parakeet TDT v2 and Silero VAD GGUFs
└── wer.py                 # scores model evaluations (docs/models/)
third_party/
└── nemo-speech/           # NeMo-Speech.cpp v0.3.0 (git submodule)
```

## App shell

`goscriber` with no subcommand starts the Fyne app (`io.github.opragmatico.goscriber`) as a menu bar / tray icon:

- **Tray menu**, laid out after Handy's: the version as a heading; Start / Stop Dictation, Cancel and Copy Last Transcript (the history's newest entry, or without a history the last dictation since launch); the current model, whose submenu lists the downloaded models with the selected one checked, switches to another (with its default quantisation when downloaded, else the first one on disk) and ends with Manage Models…, which opens the Models page; with no model selected or downloaded, "No model: Download…" in its place opens [onboarding](#onboarding)'s model step, and the dictation items are disabled. Under the model, "Loading <model>…" while one loads and Unload Model, offered while a model is in memory and idle ([model lifecycle](#model-lifecycle)); then Settings…, Check for Updates… (Goscriber has no updater, so it opens the GitHub releases page) and Quit. The shell lists the downloaded models in the background at launch and after each download or delete on the Models page. With debug mode on, a Debug submenu forces the icon state.
- **Icon per state**: idle and transcribing are template icons that follow the menu bar's colour (on Linux, whose panels keep their own colours, light grey with a dark outline); recording is red. The coordinator drives the state; post-processing and outputting show as transcribing.
- **No window at launch** while the tray is shown, except [onboarding](#onboarding) until it is over; windows open from the menu. Without a tray (`--no-tray`, the `show_tray_icon` setting, or a desktop with no tray) the settings window opens at launch, even with `--start-hidden`, and closing it quits, as in Handy.
- **macOS**: with a tray the app is an accessory app with no Dock icon. The bundle's Info.plist sets `LSUIElement`, and the app also sets the activation policy at startup, because GLFW makes every app a regular one and plain `go run` builds have no Info.plist. Without a tray the Dock icon stays as the way back in.
- **Shutdown**: Quit, SIGINT and SIGTERM all end Fyne's main loop; the app then stops following settings and closes the log.

### Single instance and remote control

Only one Goscriber runs per config directory (`GOSCRIBER_HOME` makes another), and scripts can control it (`internal/instance`, `internal/cli`):

| Command line | With Goscriber running | With none running |
| --- | --- | --- |
| `goscriber` (with or without `--start-hidden`, `--no-tray`, `--debug`) | brings the running one's settings window to the front, as it is, or on [onboarding](#onboarding) while that is not over, and exits | starts the app |
| `goscriber --toggle-transcription` | starts dictation, or stops it and pastes the transcript, as the shortcut in toggle mode; exits quietly with 0 | says Goscriber is not running and exits with 1 |
| `goscriber --cancel` | discards the dictation in progress | the same |
| `goscriber --at-login` (the [login item](#launch-at-login)'s command on Linux and Windows) | exits quietly: no window comes to the front | starts the app hidden |
| `kill -USR2 <pid>` | toggles dictation, as in Handy | |

A remote-control flag takes no other flag. The commands reach the coordinator as the tray's do, so one that does not fit the state is refused there (a toggle while a transcript is on its way), and toggling without a model raises `no_model` in the app.

**Lock.** Before the app starts, the command line takes an exclusive `flock` on `goscriber.lock` in the config directory, and holds it until the app has shut down. The kernel releases it however the process ends, so a crash never leaves Goscriber "running". Two launches at once cannot both win.

**Control socket.** The app then listens on a Unix domain socket, `goscriber.sock` beside the lock, with mode 0600; one a crash left behind is replaced. A config directory too deep for a socket address (`sun_path` holds about 100 bytes) moves the socket to `goscriber-<uid>/` under `XDG_RUNTIME_DIR` or the temporary directory, which must be the user's own and closed to others. Each connection carries one request and one response, each a line of JSON: `{"version":1,"command":"toggle-transcription"}` (or `cancel`, `show-settings`), answered by `{"ok":true}` or `{"ok":false,"error":"…"}`. A request of another protocol version, an unknown command or a malformed line is refused with the reason, which the command line prints. While the app quits, the socket goes first and commands are refused; the lock stays until the end, so a launch then waits for it and starts once it is free.

**Sending.** The command line connects to the socket. If nothing answers and no process holds the lock, Goscriber is not running. If the lock is held, the app is still starting, so it tries again every 50 ms for up to five seconds.

Windows, where this would be a named pipe, has neither yet: the app runs without the lock and the flags say they are not supported.

### Launch at login

The `autostart` setting (Settings → Advanced → Launch at login) is the OS's login item for Goscriber, which the shell keeps in step with it (`internal/app/login.go`) through `internal/platform`:

| OS | Login item | How the app tells a launch at login |
| --- | --- | --- |
| macOS 13 and later | `SMAppService.mainApp`, listed in System Settings → General → Login Items under "Open at Login"; registering it shows macOS's "Login Item Added" notification | the open-application Apple Event carries `keyAELaunchedAsLogInItem`; an observer of `NSApplicationDidFinishLaunchingNotification`, installed before Fyne starts GLFW, reads it |
| Linux | `io.github.opragmatico.goscriber.desktop` in the XDG autostart directory (`$XDG_CONFIG_HOME/autostart`, by default `~/.config/autostart`), which starts the running executable with `--at-login` ([linux.md](linux.md#launch-at-login)) | `--at-login` |
| Windows | the `Goscriber` value under `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`: `"…\goscriber.exe" --at-login` | `--at-login` |

- **Hidden.** A launch at login starts hidden, as `--start-hidden` does: behind the tray no window opens, not even unfinished onboarding, which the tray's "No model: Download…" leads back to; without a tray the settings window opens anyway. macOS tells the launch only once the app has started, so the shell plans the launch again then.
- **At launch the OS is the truth.** The user may turn the login item off outside Goscriber: in System Settings, in GNOME's startup applications (`X-GNOME-Autostart-enabled=false`), Xfce's session settings (`Hidden=true`) or Task Manager's Startup apps (an odd first byte under `…\Explorer\StartupApproved\Run`). So at launch the setting takes the login item's state. A login item that is on is written again, so it starts this executable wherever it is now: Linux and Windows record its path.
- **Changes.** Turning the setting on or off adds or removes the login item at once, on a goroutine of the shell's. When the OS refuses, a `login_item_failed` notice says why and the setting goes back to what the OS has. When macOS keeps the login item until the user allows it, the notice's button opens System Settings at Login Items. Turning it on in Goscriber also clears Task Manager's "off".
- **Only the app bundle, on macOS.** A bare executable (`go run`, `make run`) has no login item, nor has macOS before 13: the setting is disabled with the reason, and left as it is. The development bundle from `make app` has one.
- **Already running.** The login item finding Goscriber running (`--at-login`) exits quietly rather than bringing its window to the front.

## Settings window

`internal/ui`'s `SettingsWindow` is one Fyne window with a sidebar of pages, opened from the tray (Settings…; "No model: Download…" opens it on the Models page) and by notices' buttons. Light and dark follow the OS, as Fyne does by default.

| Page | What it holds |
| --- | --- |
| **General** | the dictation and cancel shortcuts (recorded by pressing them), activation mode and hold threshold, microphone, start / stop sounds with their volume and output device, mute while recording, overlay style and position |
| **Models** | a card per catalog model: what it does, size, licence, quantisation (Parakeet TDT v2 has two), status, and Download (with progress, cancellable), Delete (asks first) and Use. With no model downloaded the page says so. Below the cards, the [language picker](#language) |
| **Advanced** | app (start hidden, tray icon, launch at login, unload timeout, loading the model at launch), output (paste method, on Linux the paste keys and typing tool, clipboard, delays, trailing space, Return), transcription (accelerator, offering only what the engine was built with, live-text latency, decoding while recording, always-on microphone, VAD and its thresholds, shortest recording, silence threshold) and text (filler words, custom words with their [boost and correction threshold](#custom-vocabulary)) |
| **Permissions** | each macOS permission, with a button to System Settings when missing ([permissions.md](permissions.md)); on Linux, what a Wayland session needs set up ([linux.md](linux.md)) |
| **History** | how many dictations and how long recordings are kept; [transcribing audio files](#transcribing-files), chosen or dropped on the window, with progress and Cancel; then the latest dictations (up to 100), newest first, each with Copy, Paste again, Play, Transcribe again and Delete, and Delete all ([history](#history)) |
| **Debug** | only in debug mode: debug mode, log level, running the engine in the app ([profiling](#process-isolation)), and a viewer over the last 400 records of the logger's in-memory ring |
| **About** | Goscriber's version, the NeMo-Speech.cpp runtime's (or "not built in"), where settings and logs live, a button that runs [onboarding](#onboarding) again, and the licences of what Goscriber is built with and of the models |

- **Bound to the settings store.** Every widget shows one field of `config.Settings` and saves a change through `Store.Update` at once, so it applies to the running app and persists; a value the store refuses is logged and the widget shows the stored one again. The shell passes changes made elsewhere (the settings file, the tray) on with `SetSettings`. Each binding is declared in one line (`boolSetting`, `choiceSetting`, `sliderSetting`, `intEntrySetting` in `settings_bind.go`) with the field's JSON name and an accessor for it, and a test checks against the struct's tags that every field but `schema_version` has a widget on some page, and that each binding changes the field its name says. Sliders save when let go (or at each arrow-key step), not at every step of a drag.
- **Models.** The page reads the model files through `models.Manager` in the background each time the window opens, since a file's first look hashes it. A model can be used only once downloaded. The first model downloaded, or one downloaded while the selected model is missing, is selected: the user just chose it. Deleting the model in use unselects it, so dictation asks for a model rather than failing.
- **Shortcuts.** The shortcut field listens once clicked: hold the modifiers, press the key. It accepts what `internal/hotkey` would register for that purpose, says why otherwise (a plain letter, the other shortcut's keys), and Escape alone or clicking elsewhere stops it without a change. While it listens the global shortcuts are suspended (`hotkey.Manager.Suspend`), so pressing the current one reaches the window instead of starting dictation.
- **Debug mode.** Cmd+Shift+D (Ctrl+Shift+D elsewhere) inside the window toggles the `debug_mode` setting, which shows or hides the Debug page and the tray's Debug menu; `--debug` keeps both shown for the run.
- **Filler words.** A null `custom_filler_words` is the built-in list ([text cleanup](#text-cleanup)), shown as English's as the start of the user's own; removing the last word saves an empty list (remove none), and a button goes back to the built-in one.

`GOSCRIBER_RENDER_DIR=dir go test -run TestRenderSettingsPages ./internal/ui` writes every page, in the light and dark themes, to PNGs through Fyne's test driver, to check the layout without running the app.

## Onboarding

A new user goes from first launch to first dictation in a couple of minutes (`internal/ui/onboarding.go`). Onboarding is a mode of the settings window: its four steps replace the sidebar, under the title "Set Up Goscriber", with Skip setup and Back along the bottom.

1. **Welcome**: what Goscriber does and what setting up involves.
2. **Permissions**: the rows of the Permissions page, live (the shell's two-second permission check refreshes them while the window is open), with Allow… or Open System Settings on each one missing ([permissions.md](permissions.md)). The launch's own permission prompts wait for this step, and run when onboarding ends if it skipped them.
3. **Model**: every speech model in the catalog, with what it does (live text or not, its languages), its download size and licence. Nothing is selected or downloaded until the user presses Download; the download shares the Models page's state and progress, and, as there, the first model downloaded is selected. Continue waits for the selected model to be on disk.
4. **Try it**: the shortcut, as the General page shows it, and how to dictate with the activation mode, over a text field that has the keyboard, so the paste lands in it. Text arriving there says it works.

**When it opens.** At launch, unless onboarding is over: finished or skipped (`onboarding.done` in Fyne's app preferences). Each step shown is saved there too (`onboarding.step`), so a quit midway resumes at that step. A settings file that already selects a model (set up from the command line, or before onboarding existed) counts as over. Starting hidden with a tray leaves it closed. While it opens at launch, the launch's "no model downloaded" notice is left out. While no model is selected, the tray's "No model: Download…" and the `no_model` notice's button open its model step, so a shortcut press without a model still raises the notice, and the notice leads back here. About's Run setup again opens it at the welcome. Finishing or skipping shows the General page; Settings… from the tray leaves onboarding for the pages without ending it.

## Overlay

While a session is in progress, a small pill near the edge of the screen shows that Goscriber is listening (`internal/ui/overlay.go`): a red dot, a level meter and the elapsed time while recording, "Transcribing…" with an amber dot after it, and a cancel button in both. `overlay_style` picks how much: `none` (never shown; the default on Linux, where overlays steal focus on several desktops, as in Handy), `minimal` (the pill) or `live` (the default elsewhere: the pill widens to show the end of the transcript so far, from [live transcription](#live-transcription)). The words that are settled are drawn in full ink and the ones that may still change dimmed. `overlay_position` puts it at the top or the bottom centre of the screen that holds the mouse pointer.

The window is a borderless Fyne window (`desktop.Driver.CreateSplashWindow`, asked to stay on top), created the first time there is something to show. It must never take the keyboard from the app the user dictates into, or the paste would land in Goscriber:

- **No focus on show, on every OS**: GLFW focuses a window when it shows it unless the window was created with `GLFW_FOCUS_ON_SHOW` off. Fyne never sets that hint, so the overlay sets it (through `go-gl/glfw`, which Fyne's driver owns) only around the window's creation, then puts it back for windows created later.
- **macOS** (`platform.PrepareOverlay`, through `driver.NativeWindow.RunNative`): the `NSWindow` gets the status window level, joins all Spaces and full-screen apps (`CanJoinAllSpaces`, `FullScreenAuxiliary`, `Stationary`, `IgnoresCycle`), loses its shadow, and has its content clipped to a pill by its layer. AppKit can only pass a window's clicks through as a whole, so the overlay ignores the mouse except while the pointer is over the cancel button, which it checks ten times a second while shown (`platform.OverlayClickThrough`). Clicking cancel makes Goscriber the active app, so the overlay then hands the keyboard back (`[NSApp deactivate]`). It is placed with `NSScreen.visibleFrame`, clear of the menu bar and the Dock.
- **Elsewhere** the window is centred on the screen and Fyne's always-on-top request applies. On Linux, where the overlay is off by default, that is all there is.

The shell feeds it from the coordinator's events on the main thread: the state, the interim text at most ten times a second (always ending on the newest, and never a session's text after it ended), and the microphone level at most about 40 times a second (the recorder measures 30), with at most one level update, one interim and one clock tick waiting for the main thread at a time. If adjusting the GLFW window turns out not to be enough (it may still activate the app on some macOS versions), the fallback the issue foresaw is to draw this one overlay as a native `NSPanel` through CGo.

## Sounds

With `audio_feedback` on (off by default, as in Handy), a short rising chime plays when recording starts and a falling one when it stops (`internal/audio/feedback.go`). Both were made for Goscriber by `internal/audio/gen_sounds.go` (`go generate ./internal/audio`) and are embedded as WAV files, so they carry no third-party licence. They play through miniaudio on `output_device` (a device that is not connected falls back to the default) at `audio_feedback_volume`.

The start cue plays to its end, plus the device's buffers, before the microphone starts, so it is not transcribed; an always-on microphone's pre-roll, which heard it, is dropped (`Recorder.DropPreRoll`). The stop cue plays once the microphone has stopped, without holding up the transcription. A cancel plays nothing.

With `mute_while_recording` on, the system's default output is muted through CoreAudio after the start cue and put back as it was when the recording stops, is cancelled, fails to start or the app quits; a device that was already muted stays muted, and the device muted is the one restored even if the default changes meanwhile. Outputs without a mute switch (some HDMI and Bluetooth devices) are left alone with a warning. Muting is macOS-only for now. `goscriber debug sounds [--device D] [--volume V]` plays both cues.

## Permissions

On macOS Goscriber needs the **Microphone** permission to record and **Accessibility** for the shortcut's event tap and for posting the paste; **Input Monitoring** is not needed, because the shortcut library's tap is an active one, which Accessibility covers. At launch it asks once for each that is missing (macOS's own microphone prompt; the Accessibility dialog, remembered in Fyne's preferences so it is not shown at every launch), raises a notice with a button to System Settings for each that is refused, and shows them all on the settings window's Permissions page. At the first launch [onboarding](#onboarding)'s permissions step asks instead. Start refuses to record without the microphone permission, since macOS would deliver silence. `internal/platform` has the status API (`StatusOf`, `Request`, `PermissionStates`). Permissions attach to the app's code signature, which matters during development: [permissions.md](permissions.md).

## Shortcuts

`internal/hotkey` registers two global shortcuts from the settings (recorded on the settings window's General page): **transcribe** (`option+space` on macOS, `ctrl+space` on Windows, `ctrl+alt+space` on Linux) and **cancel** (`escape`). A shortcut is modifiers and one key, written as settings store it (`ctrl+shift+d`, `cmd+f5`); a plain letter, digit or Space is refused because a global shortcut swallows its key in every app, and only the cancel shortcut may be a plain Escape. The cancel shortcut is registered only while dictation is recording, so Escape works normally the rest of the time.

Key-downs and key-ups of the transcribe shortcut drive the activation mode: **toggle** (press to start, press to stop), **push-to-talk** (hold to record) and **hold-or-toggle**, the default (a press starts recording; released after `hold_threshold_ms`, 300 ms, it was push-to-talk and stops, released sooner it was a tap and recording goes on until the next press). The manager owns the registrations on one goroutine and re-registers when the settings change, without a restart; a shortcut that cannot be registered is logged and handed to `OnError`, and the next settings change retries it.

On macOS, `golang.design/x/hotkey` hears shortcuts through a keyboard event tap, which consumes the key and needs the **Accessibility** permission (the same one pasting needs). Registration dispatches onto the main thread, which Fyne's loop services; the manager never makes the main thread wait for it. An event tap hears no key presses while any process holds **Secure Event Input** (a focused password field, Terminal's Secure Keyboard Entry), so the app polls `IsSecureEventInputEnabled()` and logs a warning naming the holder when it stays on for 3 s, as Handy does.

On X11 the library grabs the keys on the root window (`XGrabKey`), which needs no permission. **Wayland** lets no app grab keys; the desktop owns them. Where it has the XDG GlobalShortcuts portal (KDE Plasma, Hyprland, GNOME 48 and later), `internal/hotkey/portal` is the backend: it binds the transcribe and cancel shortcuts in one portal session, which the desktop asks the user to confirm, and turns the portal's `Activated` and `Deactivated` signals into key-downs and key-ups, so every activation mode works. The backend is chosen at the first registration, on the manager's goroutine, so waiting for the portal holds up neither the launch nor the UI (`internal/app/shortcuts.go`); `Register` takes the shortcut's purpose, the portal's ID for it, and only connects that shortcut's events, so the manager may unregister and register as before without asking the desktop again. The settings window shows the keys the desktop bound. Without the portal, the user binds `pkill -USR2 -x goscriber` to a key in the desktop's keyboard settings, and the X11 grab is registered through XWayland, where it is heard in X11 windows and, on KDE Plasma, wherever legacy X11 apps may read keys. Either way the app toggles dictation on `SIGUSR2`, as Handy does, and every other `goscriber` process, the engine workers included, ignores the signal (`cmd/goscriber`). [linux.md](linux.md#the-globalshortcuts-portal) has the details.

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
| `engine_crashed` | error | no | | the engine supervisor, when a worker process crashes or stops answering ([process isolation](#process-isolation)) |
| `engine_cpu_fallback` | warning | no | | the engine supervisor, when a worker crashes or hangs while it loads or decodes on the GPU, and the engine moves to the CPU |
| `transcription_failed` | error | no | | the coordinator, when a decode fails or a model will not load for a reason other than a damaged file |
| `login_item_failed` | warning | no | Open System Settings, when macOS waits for the user to allow the login item | the shell, when the [login item](#launch-at-login) cannot be added or removed |

## Output

`internal/output` puts the final transcript into the focused app, in one of three ways (`paste_method`):

- **paste** (default): save the clipboard, write the transcript, wait `paste_delay_ms` (60 ms), send Cmd+V (Ctrl+V elsewhere), wait `paste_delay_after_ms` (60 ms) so the app reads it, then put the clipboard back. With `clipboard_handling: keep` the transcript stays on the clipboard instead. The clipboard is restored even when the keystroke fails, and not at all if something else wrote to it in the meantime (the user copied during the paste), so a newer copy is never overwritten.
- **direct**: type the transcript; the clipboard is not touched.
- **none**: copy the transcript to the clipboard and leave it there.

`append_trailing_space` adds a space after the transcript and `auto_submit` presses Return after it (paste and direct only). Deliveries are serialised, so two pastes never restore each other's transcript as the user's clipboard. `goscriber debug paste` runs a delivery from the command line, after a few seconds to switch apps.

On macOS the clipboard is `NSPasteboard` itself rather than `golang.design/x/clipboard`, which only reads text and PNG: saving copies every item in every type it offers (rich text, images, file URLs, app-private types) into Go memory, and restoring writes them all back, or leaves the clipboard empty if it was. The transcript is marked with the [nspasteboard.org](http://nspasteboard.org) transient and auto-generated types while it is there only to be pasted, so clipboard managers leave it out of their history. Key presses go through `CGEventPost` from a private event source with explicit flags, so the Option still held from Option+Space does not turn Cmd+V into another shortcut. The paste key is the one that types "v" with Command held in the active keyboard layout (`UCKeyTranslate`, read on the main thread), so Cmd+V works on Dvorak; direct typing attaches the text to key events with `CGEventKeyboardSetUnicodeString`, 20 UTF-16 units at a time. Posting needs the **Accessibility** permission: without it macOS drops the events silently, so the keyboard checks `AXIsProcessTrusted()` first and fails with `output.ErrNoPermission`.

On Linux the clipboard is the X11 `CLIPBOARD` and `PRIMARY` selections, which Goscriber owns from a hidden window and serves from a thread of its own (through XWayland on Wayland); saving keeps their text only. The paste presses `paste_keys` (Shift+Insert by default) with `typing_tool`: Goscriber's own XTEST key events on X11, or wtype, dotool or ydotool (`internal/typing`), which work on Wayland. [linux.md](linux.md#pasting-and-typing) has the details.

Handy's "reliable paste" (restore only once the target app has read the pasteboard) is a later improvement; Goscriber starts with the delays.

## The CGo engine

NeMo-Speech.cpp exposes a stable C ABI: opaque handles, size-prefixed POD config structs, explicit ownership and status codes. `internal/engine/nemo` wraps it:

- **Recognizer**: `nemo_speech_asr_create` / `_destroy`. One recognizer per loaded model. Backend `gpu = 0` uses the first device of the SDK's GPU backend (Metal, Vulkan or CUDA), `gpu = -1` the CPU. The backend is the preset's that `make deps` built, which the binding reads from the ggml backend library the process loaded (`nemo.GPUBackend()`): an SDK without one (`cpu-asr`, Linux's default) always loads with `-1`, since it refuses any GPU index, and a GPU that cannot take the model (no Vulkan device, no memory) is retried on the CPU. `ModelInfo.CPU` says where the model ended up.
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

A `GGML_ASSERT`, a GPU driver fault or an `abort()` inside C++ takes the whole Go process with it. Following Handy's `engine_supervisor`, the desktop app runs the native engine in a child process: the same `goscriber` executable started with the hidden `engine-worker` subcommand. `engine/worker.Supervisor` implements `engine.Engine` by talking to it, so the coordinator and the idle unloader see the same interface as before. `goscriber transcribe` keeps the engine in its own process: it is a one-shot command whose crash ends only itself.

**Protocol.** Each message is a frame on the worker's stdin or stdout: a little-endian `uint32` length and JSON, then a little-endian `uint32` length and the audio as raw little-endian `float32` samples (empty for most requests). The requests mirror the interface: hello (protocol version and process ID, so a worker from a rebuilt executable is refused), ping, load, unload, recognize, and stream start, push, next, endpoint, finish and close. Each carries an ID and gets one response; requests may overlap, and the worker answers each on its own goroutine, so it answers pings while it decodes. Errors cross as the engine's error kinds, so `errors.Is(err, engine.ErrModelNotFound)` works through the worker. In the worker, file descriptor 1 is pointed at stderr and the protocol uses a copy of it, so nothing the native code prints can corrupt it.

**Lifetime.** `Load` starts a worker and loads the model into it; `Unload`, or a `Load` of another model, closes its stdin, and the worker unloads and exits (it is killed after 2 s). One worker holds one model, and unloading returns all the engine's memory to the OS. With `model_unload_timeout`, `engine/autounload` sits on top of the supervisor, so an idle model's worker exits too and the next dictation starts a new one. The worker ignores Ctrl-C, which a terminal sends to the whole process group, and exits when its stdin closes, so it never outlives the app.

**Crashes and hangs.** A worker is lost when its process exits, when a request is not answered in time (3 min for a load; for a decode, 60 s or ten times the audio's length, and four times that on the CPU; 30 s for a stream call), or when it misses a heartbeat (pinged every 2 s, 10 s to answer; since pings are answered while a decode runs, a missed one means the whole process is stuck). Then the supervisor:

- logs the exit status and the worker's last 20 lines of stderr, where an assertion's message lands just before the abort;
- publishes `engine_crashed`, or `engine_cpu_fallback` for a GPU fault (below);
- starts a new worker with the same model at the next call, and retries the request that was waiting once, unless it was a decode that hung on the CPU, which is slow rather than broken;
- fails the calls of a stream that was open: a stream's state cannot be replayed, so the coordinator closes it and, on `Stop`, decodes the whole recording with `Recognize`, which runs in the new worker. The user gets the transcript and the notice.

**GPU faults.** A worker that dies or hangs while it loads or decodes on the GPU is a GPU fault. The supervisor publishes `engine_cpu_fallback` once and loads the model in every later worker on the CPU (`LoadSpec.CPU`, `gpu = -1`) until the app restarts; a load that crashed is retried on the CPU at once. A worker that dies before it is given a model, or while idle, is not blamed on the GPU. Errors the engine reports, such as a damaged model file or a runtime error, are not faults: they are passed on, and the coordinator turns them into `model_corrupt` or `transcription_failed` as before. A CPU worker still links ggml's Metal backend; only the model runs on the CPU.

**Cancel.** NeMo-Speech.cpp cannot stop a decode. A cancelled `Recognize` (the user cancelled while a recording or a chunk was decoding) gives the worker 500 ms to finish, then ends it; the next call starts a new one. A cancelled load ends its worker at once.

**Logs.** The worker writes its own log as JSON lines on stderr and the native engine prints its own lines there (`[asr] model=… backend=MTL0`). The supervisor re-logs each line through `log/slog` with the worker's process ID as the `worker` attribute: the worker's own lines keep their level, the native ones are debug, or warnings when they read like a failure.

**Cost.** Measured on an M4 Max with Metal (`TestWorkerIPCOverhead` in `internal/engine/nemo`), the fastest of 15 runs each:

| | In process | Through the worker | Overhead |
| --- | --- | --- | --- |
| Parakeet TDT 0.6B v2, `Recognize` of a 10 s clip | 57.9 ms | 58.3 ms | 0.35 ms |
| Nemotron Streaming 3.5, `Recognize` of a 10 s clip | 75.7 ms | 75.3 ms | within noise |
| Sending a 10 s clip (one `Push`, no decode) | 0.01 ms | 0.17 ms | 0.16 ms |
| Streaming 10 s in 10 ms pushes, with a `Next` after each | 496 ms | 543 ms | 47 µs per 10 ms of audio |

The GPU's own run-to-run variation is a few milliseconds, more than the hop. Starting a worker and loading a model takes 70 to 120 ms once ggml's Metal shaders are compiled; the first load after a new build compiles them, which can take 15 to 20 s, within the load timeout.

**Profiling.** `engine_in_process` (a debug setting that applies at the next launch) or `--engine-in-process` runs the engine inside the app again, as before #28, so a profiler sees it; a crash in the engine then ends the app.

### Model lifecycle

The first dictation should be as fast as the next, and an idle model should not hold its memory. The app stacks three layers over the engine: `engine/autounload` over `engine/warmup` over the supervisor (or the binding, in process), and the coordinator decides when to load.

**Loading.** With `preload_model` (on by default) the coordinator loads the selected model at launch, once the model is downloaded; loading never downloads anything, and with no model ready nothing happens. Off, the first dictation loads it, while the user speaks. `Coordinator.SyncModel` does this: the shell calls it at launch and after every settings change, and it resolves the model the settings select through the `ModelSource`. A change that selects another model, quantisation, accelerator, VAD file or threshold, or (for a streaming model) latency preset loads the new model in the background, which unloads the old one; without `preload_model` that happens only when the engine holds the old model in memory, so an idle model that was unloaded stays unloaded until the next recording. When no model is ready any more (it was deleted or unselected), the one held is unloaded. With `model_unload_timeout` at `0` nothing is preloaded: it would unload at once.

**While it loads** the tray menu shows "Loading <model>…" under the model, from the coordinator's `EventModel` (loading, ready or failed, for background loads and a recording's own). Dictation stays available: a background load runs on the engine goroutine like everything else the engine does, so a recording started meanwhile records at once (the recorder buffers its audio) and is decoded once the model is ready. No background load starts during a session, which keeps its model until the transcript is out; the end of the session brings the engine in line instead. A background load that fails raises no notice, since nobody asked to dictate, and is not tried again in the background; the next recording tries again and says why it cannot transcribe. Quitting cancels a load in progress.

**Warm-up.** On Metal the first decode after a load compiles the GPU pipelines and allocates the compute buffers: Parakeet TDT's first decode of an 11 s clip took three to five times as long as the next on an M4 Max. `engine/warmup` decodes silence right after every load, idle reloads included, so the cost moves into the load: an offline model decodes 25 s of silence, the longest chunk the coordinator cuts (`pipeline.LongestChunk`), which also sizes its buffers for every later decode (half a second compiles the pipelines but leaves a decode longer than any before it growing the buffers, 7–13 % slower); a streaming model decodes half a second whole (its fallback) and streams half a second, since dictation decodes it through a stream. On an M4 Max it takes about 160 ms for Parakeet and 120 ms for Nemotron on Metal, at no cost in memory; on the CPU, about 650 ms for Parakeet, whose footprint while loaded grows by about 180 MB. A warm-up that fails is logged and the model stays loaded. Measured by `TestModelLifecycle` in `internal/engine/nemo` (M4 Max, Metal, Silero VAD loaded, in process), the first dictation after a load takes 0.93–0.98× the median of the next five for Parakeet decoding `jfk.wav` whole, and 0.68–1.02× for Nemotron streaming it in 20 ms chunks; without a warm-up, Parakeet's took 1.3–5×.

**Idle unload.** A loaded model holds hundreds of megabytes, so `engine/autounload` unloads the model once it has been idle for `model_unload_timeout`: a number of seconds (30 by default; `0` unloads it as soon as it is idle), or `"never"`. The model is busy while a `Recognize` is in flight, a stream is open, or the coordinator holds it with `Acquire`, which it does from the start of a recording until the transcript is out. Holding it then hides a reload, and its warm-up, behind the user speaking, and a long batch recording, when the engine itself has no work, cannot outlast the timeout. An idle unload keeps the `LoadSpec`, and the next call loads the same model again; an explicit `Unload` forgets it. The tray's Unload Model is an idle unload on request (`UnloadNow`), which leaves a model in use alone; `SetOnChange` signals each time the model comes into memory or leaves it, which the tray follows to offer the item only while there is something to unload. The coordinator passes settings changes on with `SetTimeout`. With the supervisor, an unload ends the worker, so all the engine's memory goes back to the OS; in process, `TestModelLifecycle` finds the footprint back within a megabyte of where it was before the load.

**Accelerator.** `accelerator` is `auto` (Metal, or the build's GPU backend) or `cpu`, passed as `LoadSpec.CPU`; a change reloads the model like any other. A GPU fault moves every later worker to the CPU on its own ([process isolation](#process-isolation)).

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

- **Actions**: `Start`, `Stop`, `Toggle` and `Cancel` (also `Do(action)`), from the shortcut, the tray, the [command line](#single-instance-and-remote-control) and SIGUSR2. They never block: they are queued and run in order on the coordinator's own goroutine, so they are safe on the UI thread. An action that does not fit the state is refused, not queued: the shortcut pressed while a transcript is on its way does nothing.
- **Events**: `Subscribe` delivers state changes, refusals, the microphone level while recording and the transcript so far (both coalesced, so a slow reader gets the latest), and the delivered transcript. A refusal carries the unchanged state, so the shortcut's activator, which guessed the press would start or stop a recording, resyncs. The shell follows the events to drive the tray icon and to register Escape only while recording.
- **Model**: every `Start` resolves the selected model through a `ModelSource` (`CatalogModels` over the models manager: `selected_model`, in `model_quant` when the model has it, on the CPU when `accelerator` says so, with the Silero VAD when enabled and downloaded). With no model ready, `Start` records nothing and raises `no_model` afresh, so every press is answered; a damaged file raises `model_corrupt`. The engine goroutine loads the model when recording starts, if the engine does not hold it already (with `preload_model` it usually does: see [model lifecycle](#model-lifecycle)), and `Acquire`s it until the transcript is out.
- **Decode**: the recording is decoded while it goes on ([live transcription](#live-transcription)). On `Stop`, the speech gate runs first (too short or too quiet ends the session quietly), then the engine goroutine finishes the decode: the stream's final, or the chunk still open, or the whole clip when neither applies.
- **Session IDs**: each session has an ID and a context. A cancel ends the context (the decode, a text stage or the paste may stop early) and anything that finishes afterwards for that session is dropped, even while the next session is recording.
- **Stages**: `Stages{Cleanup, LLM, Hook, History}` run after recognition, each optional; the app sets `Cleanup` to `CleanupStage` ([text cleanup](#text-cleanup)) and `History` to the [history](#history); the others are nil until their issues land (#39, planned in [LLM post-processing](llm-post-processing.md), and #38). Each text stage gets the session's `SessionInfo`, with the settings the session started with. A failing text stage passes its input on; empty output pastes nothing. History saves after delivery, off the critical path.
- **Failures**: an engine error ends in `idle` with `transcription_failed`, `model_corrupt` or `no_model`; a failed delivery with `paste_failed` or `permission_missing`. Each is raised before the state returns to idle. No speech is not a failure.
- **Timings**: each stage's duration is logged at debug level, and a summary line per session (`dictation timings`); the info-level line says only whether something was delivered and how many characters, never the text.

The model is chosen on the settings window's Models page, which downloads it too. From the command line, `goscriber models pull nemotron-3.5-asr-streaming-0.6b` downloads it, and `selected_model` in `settings.json` selects it.

### Language

The language picker on the settings window's Models page offers what the selected model takes, from the catalog, by readable name (`internal/models/languages.go`):

- **Nemotron Streaming 3.5**: Automatic, the default, then its 32 locales (English (US), Spanish (Spain), Portuguese (Brazil)…). The catalog marks it `language_selection`: it transcribes the locale it is asked for, through a language prompt. The pick is saved as `language` and passed as `language_code` in every recognition request, whole or streamed. A saved language the model does not take falls back to Automatic.
- **Parakeet TDT 0.6B v2**: English, locked: it transcribes nothing else and is given no language.
- **Parakeet TDT 0.6B v3**: Automatic, locked: it detects the language itself and ignores a language it is given.

The language the model detected (`nemo_speech_asr_result_language_code`) comes back in the result's `Languages`. The coordinator hands it on with the delivered result (`EventResult.Languages`), to the text stages and history (`SessionInfo.Languages`, next to `SessionInfo.Language`, the language asked for), and to the log line of each dictation. The Models page shows the language of the last dictation under the picker until the model changes.

What NeMo-Speech.cpp v0.3.0 does with languages, checked with `testdata/audio/es.wav`, a Spanish LibriVox reading:

- **Automatic works for streams.** Handy's engine must fix a stream's language before any audio arrives. Nemotron's `auto` is a prompt of its own (index 101 of the GGUF's `asr.rnnt.prompt_dictionary`), with which the model detects the language from the audio and tags its transcript. Streamed with `auto`, the fixture comes back word for word, the same as with `es-ES` and as decoded whole, so live transcription uses the language setting as it is, with no fallback to a last detected language.
- **The tag is what the model heard.** The fixture is tagged `es-US` whether the model was asked for `es-ES` or `auto`.
- **Only finals carry it.** Interim results report no language, so the overlay cannot show it while the user speaks.
- **A wrong language costs the transcript.** Asked for `en-US`, the Spanish fixture comes back empty, which the coordinator treats as no speech. Automatic is the safer choice for anyone who dictates in more than one language.
- **Synthetic speech is unreliable.** Some macOS voices reading Spanish lose most of a sentence with `es-ES`, while others transcribe fully. That is why the fixture is a human reading.

The GGUF's prompt dictionary holds more than 100 locales and bare language codes, but the catalog lists only the 32 the model card supports.

### Custom vocabulary

Names and jargon in `custom_words` come out spelled the way the user writes them, by one of two mechanisms, depending on what the model can do (the catalog's `word_boosting` capability):

- **Word boosting** (Nemotron Streaming 3.5). Every recognition request, whole or streamed, carries the custom words as one NeMo-Speech.cpp speech context, boosted by `custom_word_boost` (1 by default, 0 to 5). The cache-aware RNNT's greedy decoder adds that score each time it emits the start of a boosted word, so it prefers them over what it would otherwise hear. The transcript is then left alone.
- **Correction** (Parakeet TDT, and Nemotron with the boost at 0). NeMo-Speech.cpp ignores speech contexts for TDT models, so the cleanup stage corrects the transcript instead (`internal/text/words.go`, adapted from Handy's): each word, or run of up to three words (the model may split "ChargeBee" into "Charge B"), is compared with each custom word by their letters and digits in lower case. The score is the edit distance divided by the longer length, and 30 % of that when it is at most a third and both have the same Soundex code. A candidate scoring under `word_correction_threshold` (0.18 by default, Handy's; 0 turns correction off) takes the closest custom word's spelling, keeping the punctuation around it and an English possessive ("Quequeg's" → "Queequeg's"). Lengths that differ by more than a quarter (and more than one letter) are never compared, and runs never cross punctuation. A custom word written with capitals keeps them; one in lower case takes a sentence's capital.

Correction is skipped after boosting, as in Handy: the boosted transcript already spells the words, and a second pass would only add the chance of turning an ordinary word into a custom one. The custom words are trimmed and de-duplicated (ignoring case) first. `goscriber transcribe` applies them the same way, with `--word` adding more.

Measured with `testdata/audio/queequeg.wav`, a LibriVox reading of *Moby-Dick* (`TestNemotronBoostsCustomWords`, `TestParakeetCorrectsCustomWords` in `internal/engine/nemo`):

| Model | Without custom words | With "Queequeg" among ten custom words |
| --- | --- | --- |
| Nemotron Streaming 3.5 | "I found Quig's arm" | "I found Queequeg's arm", whole, streamed and dictated, by boosting; the rest of the transcript unchanged |
| Parakeet TDT 0.6B v2 | "I found Quequeg's arm" | the same with boosting at any strength; "I found Queequeg's arm" after correction |

**Choosing the boost.** Higher is not better. On the fixture with ten custom words, Nemotron spells the name from a boost of 1. At 2.5 it starts writing custom words that were never said ("I had been his wife" became "Ishmael's wife"); at 4 it drops words, and at 5 the name replaces half the sentence. Over 90 s of the same reading, 1.25 already turned "I suppose" into "Ishmael", while 1 changed nothing but the name and, oddly, the capitals of "sun and shade". Hence the default of 1, and the cap at 5, which is also where NeMo-Speech.cpp clamps a boost for greedy decoding.

### Text cleanup

`CleanupStage` (`internal/pipeline/cleanup.go`) tidies every transcript with the pure functions of `internal/text`, in order: custom-word correction ([above](#custom-vocabulary)), filler-word removal, then whitespace.

**Filler words** go with `filler_word_removal`, on by default as in Handy. The built-in lists are Handy's, in two tiers: hesitations that are no word in any language the models write (`uh`, `uhm`, `umm`, `hmm`, `mmm`…) always go, while those that are words elsewhere go only from a transcript known to be in their language: `um`, `ah` and `eh` from English (`um` is "a" in Portuguese), `äh` and `ähm` from German, `euh` from French. The transcript's language (`SessionInfo.TextLanguage`) is the one the model detected, else the one it was asked for, else the model's only language (Parakeet TDT v2: English); with none known (Parakeet TDT v3, which reports nothing) only the first tier applies. `custom_filler_words` replaces both tiers when set, and an empty list removes nothing; entries may be phrases ("you know").

A filler matches whole words only, ignoring case, and also drawn out ("ummm"). The punctuation around it is tidied rather than left behind:

| Transcript | Cleaned |
| --- | --- |
| `Um, so I think we should ship it.` | `So I think we should ship it.` (the filler's comma goes, and its capital passes on) |
| `So I was, uh, thinking about this.` | `So I was, thinking about this.` (only the filler's own comma goes) |
| `It works, um.` | `It works.` (a sentence end moves to the word before, replacing its comma) |
| `Well, I think, uh. that's right` | `Well, I think, that's right` (but not before a word in lower case) |
| `It was (um) fine.` / `“Um, hello,” she said.` | `It was fine.` / `“Hello,” she said.` (brackets around the filler go; a quote it opened stays) |

**Whitespace** is collapsed to single spaces and trimmed, and a transcript with no letter or digit left (a lone "." from a short clip, or nothing but fillers) becomes empty, so nothing is pasted. `append_trailing_space` is applied by the output, not here, so the space is added once.

Settings files from before schema version 2 hold `filler_word_removal: false`, saved while the setting did nothing; the v2 migration turns it on.

### History

Every finished dictation is saved (`internal/history`, SQLite through the pure-Go `modernc.org/sqlite`), also when its delivery failed, so the text is never lost: when recording started, the delivered text, the model's raw transcript, the model, the transcript's language and the recording's length, with the recording as a 16-bit mono WAV beside the database. An entry [transcribed from a file](#transcribing-files) also keeps the file's name (schema version 2). The schema is versioned with SQLite's `user_version` and migrated forward, one transaction per step, when the app opens it; a database from a newer Goscriber is left alone, and the app runs on without saving.

**Retention.** `history_limit` is how many of the latest dictations are kept (5 by default; 0 saves none). `recording_retention` is how long their recordings are: as long as the dictation (`preserve_limit`, the default), 3 days, 2 weeks, 3 months, or none (`never`, which saves no WAV). A recording past its age is deleted and its dictation kept. The history is pruned after every dictation, when it opens, and as soon as either setting changes.

**History page.** The latest 100 dictations, newest first, each dated ("Today 14:22 · 6 s · Parakeet TDT 0.6B v2"). Copy puts the text on the clipboard. Paste again hides the settings window, so the app the user was in gets the keyboard back, and delivers the text as a dictation would. Play plays the recording on the output device in the settings, and Stop ends it. Transcribe again decodes the recording with the model the settings select now, as a file is decoded (below), runs the cleanup stage, and replaces the dictation's text and model. Delete removes a dictation and its recording; Delete all asks first.

### Transcribing files

The History page transcribes existing recordings without the command line: Choose a file… (Fyne's file dialog, filtered to `.wav` and `.flac`), or files dropped anywhere on the settings window, which switches to the page; drops during onboarding are ignored. Files go one at a time, the others waiting, with the file's name, how far it has got ("3 min 20 s of 10 min 0 s"), a progress bar and Cancel, which also drops the files waiting. Each transcript lands in the history like a dictation, named after its file ("Today 14:22 · interview.flac · 10 min 0 s · Parakeet TDT 0.6B v2"), with the file's audio as its recording, so it can be played and transcribed again; it is not pasted. Once done the page offers Copy, which matters when `history_limit` is 0 and nothing is kept.

- **Formats.** `audio.ReadFile` reads WAV (as before) and FLAC, through the pure-Go `mewkiz/flac`, told apart by their content, mixed down to mono at the file's own rate (NeMo-Speech.cpp resamples). MP3 is refused with a message to convert it: the one pure-Go decoder, `hajimehoshi/go-mp3` v0.3.4, returns distorted audio without an error for some common files (22.05 kHz at 32 kbps or less, as podcasts and audiobooks use), which would make a wrong transcript look right. `goscriber transcribe` reads FLAC too.
- **Long files.** `Coordinator.Transcribe` decodes audio of any length the way dictation decodes a recording live ([live transcription](#live-transcription)): through a stream for a streaming model (Nemotron), and in chunks of 3 to 25 s cut at pauses for an offline one (Parakeet), whatever `long_form_chunking` says. The issue proposed a single pass for Parakeet up to its 24 minutes, but NeMo-Speech.cpp splits anything past 6 min 40 s anyway, and long inputs decoded at once can come back with whole stretches missing ([long audio](models/parakeet-tdt-0.6b-v2.md#long-audio)), while chunks of that size have never failed. The custom words and the cleanup stage apply as to a dictation.
- **Sharing the engine.** The decode runs on the coordinator's engine goroutine one 10 s slice at a time, each handed to the decoder a second at a time, and progress is reported after each slice. A dictation started meanwhile queues its own work behind at most one slice, so it is not held up by a long file, and the file resumes once the dictation's decode is done (reloading its model if the dictation used another; a stream that breaks then falls back to decoding the file whole). The model is held loaded from the first slice to the end. Cancelling stops between pushes, closes the stream and leaves nothing in the history.

**Measured** (`TestTranscribeTenMinuteFile` in `internal/engine/nemo`, M4 Max, Metal, in process): a 10 min 4 s WAV, the JFK fixture 53 times over, read back from disk and transcribed word for word by both models, with 61 progress reports. Parakeet TDT 0.6B v2 took 5.4 s in chunks; Nemotron Streaming 3.5 took 22.3 s through its stream, which decodes 320 ms at a time.

### Live transcription

From the moment recording starts, the coordinator hands the recorder's 10 ms frames (`Recorder.Frames`) to a pump on the engine goroutine, queued behind the model load, so audio that arrives while the model loads waits in the recorder and nothing is lost. What the pump does depends on the model the load reported (`internal/pipeline/live.go`):

- **Streaming model** (Nemotron Streaming 3.5): it opens a `Stream` with interim results, pushes each frame and pulls every result `Next` has. Each interim is published as `EventInterim` with the whole transcript so far; its `Stable` bytes are the endpointed utterances plus the words the previous interim already had, the rest is the end that may still change. On `Stop` it calls `Finish` and drains the end-of-stream final; the finals are joined (`engine.JoinFinals`). Interims only ever add words in practice: over the fixture no hypothesis rewrote a word the one before it had.
- **Offline model** (Parakeet TDT) with `long_form_chunking` (on by default): it cuts the recording into chunks at pauses as it arrives and decodes each finished chunk with `Recognize` while the user goes on speaking; the chunks decoded so far are the interim text, all of it stable. On `Stop` only the open chunk is left to decode, and the results are joined in order with their word times moved to their place in the recording. Chunks too quiet to hold speech (`silence_threshold_db`) are skipped.
- **Offline model, chunking off**: nothing happens until `Stop`, which decodes the whole clip.

**Chunks** (`internal/pipeline/chunker.go`). NeMo-Speech.cpp runs its VAD only inside the recognizer, so pauses are found by loudness: per 20 ms window, a window is quiet below a threshold 35 % of the way (in dB) from the noise floor to the speech level, the 10th and 90th percentiles of the last 10 s, and never under −60 dBFS. A chunk ends in the middle of a pause, so no word is cut or decoded twice. It is at least 3 s long; at 3 s a 700 ms pause ends it, and the pause needed falls to 250 ms by 15 s, so short chunks end where sentences do. A chunk that reaches 25 s without one ends at the quietest 100 ms of its last 10 s. Parakeet decodes 25 s in about 140 ms on an M4 Max with Metal, and no chunk comes near the 90 s inputs that came back empty in its checks.

**Failures.** If the stream fails to start, push, decode or finish, it is closed and `Stop` decodes the whole recording with `Recognize`. If a chunk fails, nothing more is decoded live and `Stop` decodes everything from that chunk on in one piece. Either way the user gets a transcript and a warning goes to the log; only a failure of that fallback raises `transcription_failed`.

**Cancel** ends the session's context: the pump stops decoding and drains the frames until the recorder closes them, a chunk being decoded is discarded, and the stream is closed without being finished.

**Latency** (`streaming_latency`: `low`, `balanced`, the default, or `accurate`) is how far a streaming model looks ahead before deciding on a word: NeMo-Speech.cpp's `rnnt_right_context`. It is part of the model's load spec, so a change reloads the model, in the background or at the next recording ([model lifecycle](#model-lifecycle)); offline models leave it out. Measured on an M4 Max with Metal over `testdata/audio/jfk.wav` streamed in 20 ms chunks, from the end of a word (where the offline decode puts it) to the first interim holding it, in audio time:

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
- **Control socket goroutine**: accepts the [command line's](#single-instance-and-remote-control) connections and answers each on a goroutine of its own, handing the command to the coordinator (or the window to the main thread) without waiting; a signal goroutine does the same for SIGUSR2.
- **Coordinator goroutine**: runs the actions in order (opening the microphone, stopping it, refusing what does not fit the state) and owns the session's state with a mutex.
- **Engine goroutine**: one goroutine owns the engine client and serialises load, acquire and recognition, background loads included. While recording it pumps the recorder's frames into the live decode (`Push` / `Next`, or `Recognize` per chunk); on `Stop` it finishes the decode.
- **Engine worker process**: runs NeMo-Speech.cpp. In the app, the supervisor has a goroutine per worker reading its responses, one re-logging its stderr and one sending heartbeats; calls wait for their response on the caller's goroutine.
- **Session goroutine**: per session, runs the text stages, the delivery and the history save, so a slow paste, LLM or hook never holds up a cancel.
- **Background goroutines**: model downloads, history I/O, LLM requests.

Goroutines talk through channels and the coordinator; nothing outside the main thread touches a widget directly.

## Data locations

- **Settings**: `os.UserConfigDir()/goscriber/settings.json`
- **Models**: `os.UserCacheDir()/goscriber/models/` (GGUF files under their Hugging Face names, `*.part` while downloading, and `verified.json`, which remembers the files already hashed). Files already in the Hugging Face cache (`HF_HUB_CACHE`, `HF_HOME`, default `~/.cache/huggingface/hub`) are used from there.
- **History**: `os.UserConfigDir()/goscriber/history.sqlite3` and `…/recordings/*.wav`
- **Logs**: `os.UserCacheDir()/goscriber/logs/goscriber.log`
- **Single instance**: `os.UserConfigDir()/goscriber/goscriber.lock` and `…/goscriber.sock` (the [control socket](#single-instance-and-remote-control))
- **Login item** on Linux: `$XDG_CONFIG_HOME/autostart/io.github.opragmatico.goscriber.desktop` ([launch at login](#launch-at-login)); `GOSCRIBER_HOME` does not move it
