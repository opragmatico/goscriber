package ui

import (
	_ "embed"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

var (
	//go:embed assets/goscriber.svg
	appIconSVG []byte
	//go:embed assets/tray_idle.svg
	trayIdleSVG []byte
	//go:embed assets/tray_recording.svg
	trayRecordingSVG []byte
	//go:embed assets/tray_transcribing.svg
	trayTranscribingSVG []byte
)

var (
	appIcon = fyne.NewStaticResource("goscriber.svg", appIconSVG)

	// Idle and transcribing are themed: macOS draws them as template images
	// that follow the menu bar's colour, and other systems paint them in the
	// theme's foreground colour. Recording stays red everywhere so it stands
	// out.
	trayIcons = map[TrayState]fyne.Resource{
		TrayIdle:         theme.NewThemedResource(fyne.NewStaticResource("tray_idle.svg", trayIdleSVG)),
		TrayRecording:    fyne.NewStaticResource("tray_recording.svg", trayRecordingSVG),
		TrayTranscribing: theme.NewThemedResource(fyne.NewStaticResource("tray_transcribing.svg", trayTranscribingSVG)),
	}
)

// AppIcon is Goscriber's application icon, used for windows.
func AppIcon() fyne.Resource { return appIcon }

// TrayIcon returns the tray icon for a state; unknown states get the idle icon.
func TrayIcon(s TrayState) fyne.Resource {
	if icon, ok := trayIcons[s]; ok {
		return icon
	}
	return trayIcons[TrayIdle]
}
