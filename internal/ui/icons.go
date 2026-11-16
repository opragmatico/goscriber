package ui

import (
	"bytes"
	_ "embed"
	"runtime"

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

	// Recording stays red everywhere so it stands out.
	trayIcons = trayIconsFor(runtime.GOOS)
)

// trayIconsFor returns the tray icons for goos. On macOS idle and
// transcribing are themed: macOS draws them as template images that follow
// the menu bar's colour. A Linux tray (a StatusNotifierItem) gets pixels
// painted with the app's theme instead, while the panel has its own
// colours, dark on GNOME whatever the theme; so there they are light grey
// with a dark outline, which shows on dark and light panels alike.
func trayIconsFor(goos string) map[TrayState]fyne.Resource {
	recording := fyne.NewStaticResource("tray_recording.svg", trayRecordingSVG)
	if goos == "linux" {
		return map[TrayState]fyne.Resource{
			TrayIdle:         panelIcon("tray_idle.svg", trayIdleSVG),
			TrayRecording:    recording,
			TrayTranscribing: panelIcon("tray_transcribing.svg", trayTranscribingSVG),
		}
	}
	return map[TrayState]fyne.Resource{
		TrayIdle:         theme.NewThemedResource(fyne.NewStaticResource("tray_idle.svg", trayIdleSVG)),
		TrayRecording:    recording,
		TrayTranscribing: theme.NewThemedResource(fyne.NewStaticResource("tray_transcribing.svg", trayTranscribingSVG)),
	}
}

// panelIcon paints a monochrome icon light grey with a dark outline.
func panelIcon(name string, svg []byte) fyne.Resource {
	painted := bytes.ReplaceAll(svg, []byte("<path "),
		[]byte(`<path fill="#e8e8e8" stroke="#1e1e1e" stroke-opacity="0.65" stroke-width="0.6" `))
	return fyne.NewStaticResource(name, painted)
}

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
