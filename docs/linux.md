# Linux

Goscriber runs on mainstream Linux desktops, on X11 and on Wayland. On X11 it hears the shortcut and pastes by itself. Wayland lets no app take a global shortcut or press keys in another app, so there the desktop binds Goscriber's shortcuts, through its global shortcuts portal or as a shortcut in its own keyboard settings, and a small program presses the paste keys. This page covers the setup on Ubuntu 24.04 (GNOME, Wayland by default) and on an X11 session, and why Goscriber works the way it does on Linux.

## Setup

Install the package and start Goscriber from the app grid (it lives in the tray afterwards):

```bash
sudo apt install ./goscriber_*.deb
```

The tarball runs from wherever it is unpacked: `goscriber-VERSION/bin/goscriber`.

The first launch walks through choosing and downloading a model, as on macOS. What else to set up depends on the session, which `echo $XDG_SESSION_TYPE` names and Settings → Permissions describes:

### X11

Nothing else. The dictation shortcut (`Ctrl+Alt+Space` by default) works in every app with every activation mode, and the paste keys go out as X11 key events (XTEST), which need no other program.

### GNOME on Wayland (Ubuntu 24.04)

1. **The shortcut.** GNOME 46, Ubuntu 24.04's, has no global shortcuts portal. In Settings → Keyboard → Keyboard Shortcuts → View and Customise Shortcuts → Custom Shortcuts, add one named "Goscriber dictation" with the command below, and give it a key such as `Ctrl+Alt+Space`. Each press starts or stops dictation (toggle; push-to-talk needs the key's release, which a desktop shortcut does not report). On GNOME 48 and later (Ubuntu 25.04 and later) skip this step: the desktop binds the shortcut itself, as below.

   ```text
   pkill -USR2 -x goscriber
   ```

2. **Something to press the paste keys.** GNOME has no virtual keyboard for apps, so wtype does not work there; ydotool and dotool do, through the kernel's `/dev/uinput`. Ubuntu 24.04 packages ydotool 0.1.8, with its daemon apart:

   ```bash
   sudo apt install ydotool ydotoold
   ```

   ydotool needs `/dev/uinput`, which only root may open by default. Let your user open it, then log out and back in:

   ```bash
   echo 'KERNEL=="uinput", GROUP="input", MODE="0660", OPTIONS+="static_node=uinput"' | sudo tee /etc/udev/rules.d/80-uinput.rules
   sudo usermod -aG input "$USER"
   ```

   Run its daemon, `ydotoold`, in your session. Without it ydotool 0.1 creates a new virtual keyboard for each paste, whose first keys the desktop may miss, and ydotool 1.0 (Debian 13, newer Ubuntu releases) does not work at all. ydotool 1.0's package ships a user service (`systemctl --user enable --now ydotool`); for Ubuntu 24.04's, add one:

   ```bash
   mkdir -p ~/.config/systemd/user
   printf '[Unit]\nDescription=ydotool daemon\n\n[Service]\nExecStart=/usr/bin/ydotoold\nRestart=on-failure\n\n[Install]\nWantedBy=default.target\n' > ~/.config/systemd/user/ydotoold.service
   systemctl --user enable --now ydotoold
   ```

   Goscriber speaks both versions: it tries 1.0's key codes and falls back to 0.1's key names, then remembers which worked. Check it from a terminal: `goscriber debug paste --tool ydotool "it works"` pastes into whatever window has the focus after three seconds.

Settings → Permissions and onboarding's permissions step show the same instructions, with the typing tools that are installed.

### KDE Plasma, Hyprland and GNOME 48 or later

These desktops have the [global shortcuts portal](#the-globalshortcuts-portal). At the first launch the desktop asks you to confirm Goscriber's two shortcuts, "Start and stop dictation" (with the dictation shortcut from the settings, `Ctrl+Alt+Space` by default, suggested) and "Cancel dictation", and keeps your answer for later launches. Every activation mode works, push-to-talk included. Settings → General → Desktop keys shows the keys the desktop bound; change them in the desktop's keyboard settings, or with the Change… button where the desktop's portal offers its dialog for it.

Start Goscriber from the app grid, or its desktop file, at least the first time: the portal knows the app by its desktop file, `io.github.opragmatico.goscriber.desktop`, which the packages install. If you bound `pkill -USR2 -x goscriber` to the same keys before, remove that shortcut, or the two will clash.

Hyprland asks nothing: bind the shortcuts in its configuration with the `global` dispatcher (see Hyprland's wiki on global keybinds); their IDs are `transcribe` and `cancel`.

Sway and other wlroots compositors have no global shortcuts portal: bind the command there, such as `bindsym Ctrl+Alt+space exec pkill -USR2 -x goscriber`. The same works on KDE Plasma (System Settings → Shortcuts → Add New → Command or Script) for toggle only, if you prefer it to the portal.

KDE Plasma, Sway and Hyprland support wtype, which needs no setup: `sudo apt install wtype` (or your distribution's equivalent).

## Shortcuts

| Session | How the shortcut is heard | Activation modes |
| --- | --- | --- |
| X11 | `golang.design/x/hotkey`: `XGrabKey` on the root window, for each Caps Lock and Num Lock state | all |
| Wayland with the GlobalShortcuts portal (KDE Plasma, Hyprland, GNOME 48 and later) | the desktop binds the keys and reports their presses and releases over D-Bus | all |
| Wayland without it (GNOME 46, Sway) | the desktop's shortcut runs `pkill -USR2 -x goscriber`, and the app toggles dictation on `SIGUSR2`, as Handy does | toggle |

`SIGUSR2` works in every session, with or without the portal. Without the portal Goscriber also registers its shortcut with XWayland. It is heard while an X11 window has the focus, and on KDE Plasma in every app when System Settings → Applications → Legacy X11 App Support lets X11 apps read keys typed with modifiers. A desktop shortcut on the same keys takes precedence, so the two never both fire.

`SIGUSR2` reaches every process named `goscriber`: the engine workers, `goscriber transcribe` and the other subcommands ignore it, so only the app reacts. `goscriber --toggle-transcription` does the same through the running app's control socket, and `goscriber --cancel` discards the recording: either can be the desktop shortcut's command where `goscriber` is on the `PATH`, as the .deb puts it.

### The GlobalShortcuts portal

The XDG desktop portal's [GlobalShortcuts](https://flatpak.github.io/xdg-desktop-portal/docs/doc-org.freedesktop.portal.GlobalShortcuts.html) interface is the Wayland way for an app to own a shortcut, and Goscriber prefers it on Wayland (`internal/hotkey/portal`):

- **Finding it.** At launch on Wayland Goscriber looks on the session bus for the portal's GlobalShortcuts, version 1 or later, for at most five seconds. Without it, it falls back to the XWayland grab and `SIGUSR2`, and the settings window says to bind the command.
- **Binding.** Goscriber first tells the portal its app ID through the host app registry (xdg-desktop-portal 1.19 and later), which a program the desktop did not start from its desktop file needs; an older portal, or one that already knows the app, refuses, and nothing changes. It then creates a portal session and binds both shortcuts in one call, because a session may bind only once: `transcribe` ("Start and stop dictation") and `cancel` ("Cancel dictation"), each with the settings' shortcut as a preferred trigger in the freedesktop shortcuts format (`CTRL+ALT+space`). A key without modifiers other than F1–F20 is not suggested, since the desktop would take it from every app for as long as Goscriber runs: the default cancel shortcut, Escape, is bound without keys, for the user to give it some in the desktop's settings.
- **The keys are the desktop's.** The desktop asks the first time and keeps the answer; later sessions bind without asking, and suggestions no longer change anything. Changing the shortcut in Goscriber's settings therefore changes nothing while the portal holds it: the General page shows the desktop's keys instead, with a Change… button that opens the desktop's dialog through `ConfigureShortcuts` where the portal has version 2.
- **Key events.** The portal signals `Activated` and `Deactivated`, which become the key-down and key-up of the hotkey manager, so every activation mode works. The cancel shortcut's presses count only while recording, as everywhere, though the desktop holds its keys all along.
- **When it fails.** If the user dismisses the dialog, or the portal fails, a notice says so and offers the settings; `SIGUSR2` still works, and the next launch asks again. Quitting closes the portal session, which gives the keys back.
- **Where it works.** KDE Plasma (5.27 and 6), Hyprland, and GNOME from version 48 (Ubuntu 25.04 and later). Not GNOME 46, Ubuntu 24.04's, and not Sway or other wlroots compositors.

The tests run the backend against a fake portal on a private `dbus-daemon`, and skip where `dbus-daemon` is not installed.

## Pasting and typing

`internal/platform` puts the transcript on the clipboard and presses the paste keys, as on macOS ([output](architecture.md#output)), with these differences:

- **Clipboard.** X11 keeps no clipboard of its own: the app that copied holds the data. Goscriber owns `CLIPBOARD` and `PRIMARY` from a hidden window and answers the apps that paste from a thread of its own. On Wayland this goes through XWayland, whose selections GNOME, KDE and wlroots compositors pass to Wayland apps. The transcript is marked with `x-kde-passwordManagerHint` while it is there only to be pasted, so Klipper and other clipboard managers leave it out of their history.
- **Restoring the clipboard.** Goscriber keeps the text the clipboard held and puts that back. Rich text comes back as plain text, and an image or files copied before a dictation are not put back (the transcript stays on the clipboard). `PRIMARY` is put back only if no other selection was made meanwhile.
- **Paste keys** (`paste_keys`): `Shift+Insert` by default, as in Handy, because it pastes in terminals as well as in other apps and does not depend on the keyboard layout; `Ctrl+V` and `Ctrl+Shift+V` are the alternatives. Some apps read the clipboard only after handling the keys, so Linux waits 500 ms (`paste_delay_after_ms`) before putting the clipboard back.
- **Typing tool** (`typing_tool`): what presses the keys. `auto` uses X11 key events in an X11 session and, on Wayland, the first of wtype, dotool and ydotool that works, remembering it. Each can also be chosen.

| Tool | Works on | Setup |
| --- | --- | --- |
| built in (XTEST) | X11; on Wayland, X11 windows only | none |
| `xdotool` | X11; on Wayland, X11 windows only | `sudo apt install xdotool` |
| `wtype` | Wayland compositors with the virtual keyboard protocol: Sway, Hyprland, KDE Plasma; not GNOME | `sudo apt install wtype` |
| `dotool` | any session | the user in the `input` group; packaged by some distributions, not Ubuntu 24.04 |
| `ydotool` | any session | `/dev/uinput` access and the `ydotoold` daemon; version 0.1 (Ubuntu 24.04) and 1.0 both work |

**Direct typing** (`paste_method: direct`) types the characters instead of pasting. The built-in typing uses the keys of the current layout, with Shift where needed, and maps a spare key code to any character the layout lacks for the moment, as xdotool does; Caps Lock is turned off while it types. The Wayland tools type as their own keyboard: wtype handles any character, dotool and ydotool are safest with what a US keyboard types. Pasting is the more reliable method on Linux.

`goscriber debug paste [--tool T] [--keys K] TEXT` runs one delivery from a terminal with any tool and paste keys, and says what pressed them.

## Speech engine: CPU and Vulkan

`make deps` builds NeMo-Speech.cpp's `cpu-asr` preset on Linux. For the GPU, build the `vulkan-asr` preset, which needs the Vulkan headers and shader compiler at build time and a Vulkan driver (Mesa or the GPU vendor's) at run time:

```bash
sudo apt-get install libvulkan-dev glslc spirv-headers
NEMO_SPEECH_PRESET=vulkan-asr make deps build
```

The binding tells the builds apart by the ggml backend library they load (`nemo.GPUBackend()`, [details](engine-sdk.md#linking-from-go)):

- **A CPU-only build** loads every model on the CPU. Settings → Advanced → Run the model on says so and offers nothing else; asking such a build for a GPU would fail to load the model.
- **A Vulkan build** offers "The GPU when there is one (Vulkan), else the CPU" and "The CPU only". When there is no Vulkan device, or the GPU cannot take the model, the model loads on the CPU instead, and the engine worker logs it.
- **About** shows the backend next to the engine's version, such as "0.3.0 (Vulkan)".

A GPU driver that crashes the engine worker moves later loads to the CPU, as on macOS ([process isolation](architecture.md#process-isolation)). CUDA (`cuda-asr`) builds the same way with the CUDA toolkit, untested.

## Overlay and tray

- **Overlay**: off by default (`overlay_style: none`), as in Handy: on several desktops a new window takes the keyboard focus, so the paste would land in the overlay. Turned on, it is centred on the screen. Audio feedback (`audio_feedback`) is the alternative for knowing when recording starts.
- **Tray**: Fyne's tray is a StatusNotifierItem over D-Bus, which KDE Plasma, Xfce, Cinnamon and wlroots bars show. GNOME needs the AppIndicator extension, which Ubuntu installs and turns on; on other GNOME systems install `gnome-shell-extension-appindicator`. Without a tray, the settings window opens at launch and closing it quits. The idle and transcribing icons are light grey with a dark outline, which shows on GNOME's dark top bar and on light panels alike.

## Packages

Decision: a **tarball** and a **`.deb`**, not an AppImage. The tarball runs anywhere with a recent glibc; the `.deb` covers Ubuntu and Debian, installs the desktop entry and icon, and lets apt pull in the libraries the binary links (OpenGL, X11, XTEST, libgomp and, in a Vulkan build, libvulkan), which `dpkg-shlibdeps` reads from the binaries. An AppImage needs FUSE 2, which Ubuntu 24.04 no longer installs by default, and would carry the same files as the tarball.

```bash
NEMO_SPEECH_CMAKE_ARGS=-DGGML_NATIVE=OFF make deps   # portable CPU code for other machines
make dist-linux                                      # dist/goscriber-VERSION-linux-ARCH.tar.gz and goscriber_VERSION_ARCH.deb
```

Both hold `bin/goscriber` and the NeMo-Speech.cpp and ggml libraries in `lib/`, found through a `$ORIGIN/../lib` run path set with patchelf; the `.deb` installs them in `/opt/goscriber`, links `/usr/bin/goscriber`, and adds the desktop entry, the icon, the licences and this page. Build them on the oldest distribution to support: the libraries need its glibc and libstdc++ or newer. Each release carries both, built on Ubuntu 24.04 for x86-64 and arm64 with the CPU engine ([releasing.md](releasing.md#linux-packages)).

## Building from source

```bash
sudo apt-get install gcc pkg-config libgl1-mesa-dev xorg-dev libxkbcommon-dev libwayland-dev libxtst-dev \
    build-essential cmake ninja-build git
make deps build
make run
```

`xorg-dev` includes the XTEST headers (`libxtst-dev`). `make test` runs every test; the X11 clipboard and key-event tests in `internal/platform` need an X server and skip without one, so run them under Xvfb:

```bash
sudo apt-get install xvfb
xvfb-run -a go test -race ./internal/platform/...
```

They play another app with `internal/platform/x11test`: a focused window that records what is typed into it and pastes the clipboard when it gets the paste keys, as a text field does.
