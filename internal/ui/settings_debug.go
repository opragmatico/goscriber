package ui

import (
	"runtime"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/config"
)

// logLines is how many of the most recent log records the viewer shows.
const logLines = 400

// logsState is the Debug page's log viewer.
type logsState struct {
	text   *widget.Label
	scroll *container.Scroll
}

// debugShortcutName names DebugShortcut the way this OS writes it.
func debugShortcutName() string {
	if runtime.GOOS == "darwin" {
		return "Cmd+Shift+D"
	}
	return "Ctrl+Shift+D"
}

func (w *SettingsWindow) debugPage() fyne.CanvasObject {
	w.logs.text = widget.NewLabel("")
	w.logs.text.TextStyle.Monospace = true
	w.logs.text.Selectable = true
	w.logs.scroll = container.NewScroll(w.logs.text)
	w.logs.scroll.SetMinSize(fyne.NewSize(0, 260))
	refresh := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), w.refreshLogs)
	buttons := container.NewHBox(refresh)
	if w.opts.OpenFolder != nil && w.opts.Info.LogsDir != "" {
		buttons.Add(widget.NewButtonWithIcon("Open the logs folder", theme.FolderOpenIcon(),
			func() { w.opts.OpenFolder(w.opts.Info.LogsDir) }))
	}

	return settingsPage("Debug",
		paragraph("Shown in debug mode. "+debugShortcutName()+" in this window turns debug mode on and off."),
		group("Debug",
			w.row("Debug mode", "Shows this page and the tray's Debug menu. --debug turns it on for one run.",
				boolSetting(w, "debug_mode", func(s *config.Settings) *bool { return &s.DebugMode })),
			w.row("Log level", "The least serious messages written to the log.",
				choiceSetting(w, "log_level", func(s *config.Settings) *config.LogLevel { return &s.LogLevel },
					[]choice[config.LogLevel]{
						{config.LogDebug, "Debug"},
						{config.LogInfo, "Info"},
						{config.LogWarn, "Warnings"},
						{config.LogError, "Errors"},
					}, nil)),
			w.row("Engine in the app", "Runs the speech engine inside Goscriber instead of a worker process, for profiling. A crash in the engine then closes the app. Takes effect at the next launch.",
				boolSetting(w, "engine_in_process", func(s *config.Settings) *bool { return &s.EngineInProcess })),
		),
		section("Log", w.logs.scroll),
		buttons,
		path(w.opts.Info.LogsDir),
	)
}

// refreshLogs shows the most recent log records.
func (w *SettingsWindow) refreshLogs() {
	if w.logs.text == nil {
		return
	}
	var lines []string
	if w.opts.Logs != nil {
		entries := w.opts.Logs()
		if len(entries) > logLines {
			entries = entries[len(entries)-logLines:]
		}
		for _, e := range entries {
			lines = append(lines, e.Line)
		}
	}
	if len(lines) == 0 {
		lines = []string{"Nothing logged yet."}
	}
	w.logs.text.SetText(strings.Join(lines, "\n"))
	w.logs.scroll.ScrollToBottom()
}
