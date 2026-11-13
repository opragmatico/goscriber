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
	//go:embed assets/tray_attention.svg
	trayAttentionSVG []byte
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

// trayAttention replaces the idle icon while a persistent problem, such as a
// missing permission or model, needs the user. It is amber everywhere, like
// the red recording icon, so it stands out.
var trayAttention = fyne.NewStaticResource("tray_attention.svg", trayAttentionSVG)

// TrayAttentionIcon is the idle icon with a problem waiting.
func TrayAttentionIcon() fyne.Resource { return trayAttention }

// AppIcon is Goscriber's application icon, used for windows.
func AppIcon() fyne.Resource { return appIcon }

// TrayIcon returns the tray icon for a state; unknown states get the idle icon.
func TrayIcon(s TrayState) fyne.Resource {
	if icon, ok := trayIcons[s]; ok {
		return icon
	}
	return trayIcons[TrayIdle]
}
