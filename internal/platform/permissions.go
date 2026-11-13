package platform

import "fmt"

// Permission is a macOS privacy permission.
type Permission string

// Permissions Goscriber knows about.
const (
	// PermMicrophone lets Goscriber record. Without it macOS hands the app
	// silence rather than an error.
	PermMicrophone Permission = "microphone"
	// PermAccessibility lets Goscriber post key presses (the paste and
	// direct typing) and run the keyboard event tap the global shortcut
	// listens through.
	PermAccessibility Permission = "accessibility"
	// PermInputMonitoring lets a process listen to keyboard events with a
	// listen-only event tap. The shortcut library's tap is an active one,
	// which Accessibility covers, so Goscriber does not need it today; a
	// listen-only backend for modifier-only shortcuts would.
	PermInputMonitoring Permission = "input_monitoring"
)

// Permissions lists every permission, in the order onboarding asks for them.
var Permissions = []Permission{PermMicrophone, PermAccessibility, PermInputMonitoring}

// Title names the permission as System Settings does.
func (p Permission) Title() string {
	switch p {
	case PermMicrophone:
		return "Microphone"
	case PermAccessibility:
		return "Accessibility"
	case PermInputMonitoring:
		return "Input Monitoring"
	default:
		return string(p)
	}
}

// Why says what Goscriber needs the permission for, in a sentence.
func (p Permission) Why() string {
	switch p {
	case PermMicrophone:
		return "Goscriber records your voice while you hold the shortcut. Audio never leaves this computer."
	case PermAccessibility:
		return "Goscriber hears the global shortcut and pastes the transcript into the app you are using."
	case PermInputMonitoring:
		return "Not needed with the current shortcut support."
	default:
		return ""
	}
}

// Required reports whether Goscriber needs the permission as it works now.
func (p Permission) Required() bool { return p != PermInputMonitoring }

// SettingsURL opens System Settings at the permission's pane in Privacy &
// Security.
func (p Permission) SettingsURL() string {
	pane := map[Permission]string{
		PermMicrophone:      "Privacy_Microphone",
		PermAccessibility:   "Privacy_Accessibility",
		PermInputMonitoring: "Privacy_ListenEvent",
	}[p]
	return "x-apple.systempreferences:com.apple.preference.security?" + pane
}

// PermissionStatus is where a permission stands.
type PermissionStatus int

// Permission statuses.
const (
	// StatusNotNeeded: the system has no such permission (not macOS).
	StatusNotNeeded PermissionStatus = iota
	// StatusNotDetermined: the user has not been asked yet.
	StatusNotDetermined
	// StatusGranted: the user allowed it.
	StatusGranted
	// StatusDenied: the user refused it, or turned it off.
	StatusDenied
	// StatusRestricted: a device policy (parental controls, MDM) forbids
	// it; the user cannot grant it.
	StatusRestricted
)

func (s PermissionStatus) String() string {
	switch s {
	case StatusNotNeeded:
		return "not needed"
	case StatusNotDetermined:
		return "not asked yet"
	case StatusGranted:
		return "granted"
	case StatusDenied:
		return "not granted"
	case StatusRestricted:
		return "restricted"
	default:
		return fmt.Sprintf("PermissionStatus(%d)", int(s))
	}
}

// Usable reports whether the permission lets Goscriber work: granted, or
// not a thing on this system.
func (s PermissionStatus) Usable() bool { return s == StatusGranted || s == StatusNotNeeded }

// PermissionState is a permission and where it stands, for onboarding and
// the settings window.
type PermissionState struct {
	Permission Permission
	Status     PermissionStatus
}

// PermissionStates returns the status of every permission. It never shows a
// prompt.
func PermissionStates() []PermissionState {
	out := make([]PermissionState, len(Permissions))
	for i, p := range Permissions {
		out[i] = PermissionState{Permission: p, Status: StatusOf(p)}
	}
	return out
}

// micStatus maps AVAuthorizationStatus (NotDetermined 0, Restricted 1,
// Denied 2, Authorized 3) to a PermissionStatus.
func micStatus(av int) PermissionStatus {
	switch av {
	case 0:
		return StatusNotDetermined
	case 1:
		return StatusRestricted
	case 3:
		return StatusGranted
	default:
		return StatusDenied
	}
}
