# macOS permissions

Goscriber needs two of macOS's privacy permissions. Both live in System Settings → Privacy & Security, and both are granted to an app identity, not to a binary on disk.

| Permission | Why | Without it | Asked for |
| --- | --- | --- | --- |
| **Microphone** | Recording. | macOS hands the app silence, not an error. Dictation refuses to start and raises "Microphone access needed". | At launch, while macOS has never asked: its own prompt, once. |
| **Accessibility** | The global shortcut (an active keyboard event tap) and pasting or typing (posted key events). | The shortcut does not register and pastes are dropped. "Permission needed" is raised. | At launch, once: macOS's dialog offering to open System Settings. Goscriber remembers it asked (in its Fyne preferences) and afterwards leaves it to the notice's button. |
| **Input Monitoring** | Only listen-only event taps. | Nothing today: the shortcut library's tap is an active one, which Accessibility covers. | Never. The status is reported, for a future listen-only backend (modifier-only shortcuts). |

Each missing permission raises a notice whose button opens System Settings at the right pane (`x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone`, `…Privacy_Accessibility`, `…Privacy_ListenEvent`). The tray turns amber and its menu starts with the same link. Every two seconds the app checks again. Once a permission is granted its notice ends, and Accessibility also re-registers the shortcut, without a restart.

The settings window's **Permissions** page shows where each one stands, with the same buttons. On other systems it says no permission is needed. The status API behind it is `platform.StatusOf`, `platform.Request` and `platform.PermissionStates`, ready for onboarding (#31).

A recording that is nothing but exact zeros, which is what macOS delivers without the permission, also logs a warning naming the permission and the input device.

## During development

macOS attributes a permission to the *responsible* app:

- **`make run`, `go run`, `./goscriber` from a terminal**: the terminal app (Terminal, iTerm, VS Code) asks for and holds the permissions, and every program it starts shares them. That is the quickest way to try dictation, but it grants your terminal the microphone and Accessibility.
- **`make app`, then `open dist/Goscriber.app`**: Goscriber asks for itself, under its bundle identifier `io.github.opragmatico.goscriber`, with the microphone description from `packaging/macos/Info.plist`. This is what users get.

macOS ties the grant to the bundle's code signature. `make app` signs ad hoc by default (`CODESIGN_IDENTITY=-`). An ad-hoc signature changes with every build, so after a rebuild macOS asks again, and the Accessibility list may show a stale Goscriber entry that no longer applies. To keep the permissions across rebuilds, sign with a stable identity, for example a free Apple Development certificate from Xcode:

```bash
make app CODESIGN_IDENTITY="Apple Development: Your Name (TEAMID)"
```

To start over as a new user would:

```bash
tccutil reset Microphone io.github.opragmatico.goscriber
```

```bash
tccutil reset Accessibility io.github.opragmatico.goscriber
```

```bash
rm ~/Library/Preferences/fyne/io.github.opragmatico.goscriber/preferences.json
```

The last one forgets that Goscriber already showed the Accessibility dialog: Fyne keeps the app's preferences in that file.
