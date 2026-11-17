package platform

import "errors"

// Launching at login. Goscriber's login item is its entry among the apps
// the OS opens when the user logs in:
//
//   - macOS 13 and later: SMAppService.mainApp, which System Settings →
//     General → Login Items lists under "Open at Login". Only an app bundle
//     can have one.
//   - Linux: an XDG autostart desktop file, ~/.config/autostart/
//     io.github.opragmatico.goscriber.desktop.
//   - Windows: a value named Goscriber under HKCU\Software\Microsoft\
//     Windows\CurrentVersion\Run.
//
// On Linux and Windows the login item runs Goscriber with AtLoginFlag. A
// macOS login item passes no arguments, so LaunchedAtLogin reads how macOS
// opened the app instead.

// AtLoginFlag is the command-line flag the login item starts Goscriber with
// on Linux and Windows. internal/cli parses it; it cannot import this
// package, which needs CGo.
const AtLoginFlag = "--at-login"

// loginItemID names the login item: the app ID, as app.ID, which is also
// the name of the desktop file the Linux packages install.
const loginItemID = "io.github.opragmatico.goscriber"

// ErrLoginItemUnsupported is what LoginItemSupported and the other login
// item functions return, wrapped with the reason, where Goscriber cannot
// launch at login: before macOS 13, from a bare executable on macOS (only
// an app bundle has a login item), or on a system without support.
var ErrLoginItemUnsupported = errors.New("launching at login is not supported")

// ErrLoginItemNeedsApproval is what SetLoginItem returns when macOS keeps
// the login item until the user allows it in System Settings → General →
// Login Items (LoginItemsSettingsURL).
var ErrLoginItemNeedsApproval = errors.New("allow Goscriber to open at login in System Settings → General → Login Items")

// LoginItemsSettingsURL opens the Login Items pane of System Settings
// (macOS 13 and later).
const LoginItemsSettingsURL = "x-apple.systempreferences:com.apple.LoginItems-Settings.extension"

// unsupportedError says why launching at login is not available. Its text is
// the reason alone, so it reads well in the settings window and the log, and
// it matches ErrLoginItemUnsupported.
type unsupportedError string

func (e unsupportedError) Error() string        { return string(e) }
func (e unsupportedError) Is(target error) bool { return target == ErrLoginItemUnsupported }

// windowsRunCommand is the command line the Run registry value holds: the
// executable, quoted because paths such as C:\Program Files have spaces,
// and AtLoginFlag. A Windows path cannot contain a double quote.
func windowsRunCommand(exe string) string { return `"` + exe + `" ` + AtLoginFlag }
