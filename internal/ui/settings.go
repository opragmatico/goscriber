package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Page is a page of the settings window.
type Page int

// Settings pages, in sidebar order.
const (
	PageGeneral Page = iota
	PageModels
	PageAbout
)

// SettingsTitle is the settings window's title.
const SettingsTitle = "Goscriber Settings"

// SettingsInfo is what the placeholder pages show.
type SettingsInfo struct {
	Version      string
	SettingsFile string
	LogsDir      string
}

// SettingsWindow is a placeholder for the settings window: it has the pages
// the tray links to, each with a short note, until the real pages land. The
// Fyne window is created on first Show, so nothing is on screen until the user
// asks for it. Its methods must be called on the Fyne main thread.
type SettingsWindow struct {
	app     fyne.App
	info    SettingsInfo
	onClose func()
	model   string

	win        fyne.Window
	tabs       *container.AppTabs
	modelLabel *widget.Label
}

// NewSettingsWindow prepares the window. onClose, if not nil, runs after the
// user closes the window, which only hides it so it can be shown again.
func NewSettingsWindow(a fyne.App, info SettingsInfo, onClose func()) *SettingsWindow {
	return &SettingsWindow{app: a, info: info, onClose: onClose}
}

// Show opens the window on page p and brings it to the front.
func (s *SettingsWindow) Show(p Page) {
	if s.win == nil {
		s.create()
	}
	s.tabs.SelectIndex(int(p))
	s.win.Show()
	s.win.RequestFocus()
}

// Window is the Fyne window, or nil before the first Show.
func (s *SettingsWindow) Window() fyne.Window { return s.win }

// Page is the page currently selected.
func (s *SettingsWindow) Page() Page {
	if s.tabs == nil {
		return PageGeneral
	}
	return Page(s.tabs.SelectedIndex())
}

// SetModel sets the display name of the ready model; "" means none is ready.
func (s *SettingsWindow) SetModel(name string) {
	s.model = name
	if s.modelLabel != nil {
		s.modelLabel.SetText(s.modelText())
	}
}

func (s *SettingsWindow) modelText() string {
	if s.model == "" {
		return "No model is downloaded yet. Choosing and downloading a model will happen on this page."
	}
	return "Selected model: " + s.model
}

func (s *SettingsWindow) create() {
	s.modelLabel = paragraph(s.modelText())
	s.tabs = container.NewAppTabs(
		container.NewTabItem("General", page("General",
			paragraph("Goscriber runs in the menu bar. This window is a placeholder: the settings pages are not built yet."),
			paragraph("Settings are saved in:"),
			path(s.info.SettingsFile),
		)),
		container.NewTabItem("Models", page("Models", s.modelLabel)),
		container.NewTabItem("About", page("Goscriber",
			paragraph("Offline push-to-talk speech-to-text."),
			paragraph("Version "+s.info.Version),
			paragraph("Logs are written to:"),
			path(s.info.LogsDir),
		)),
	)
	s.tabs.SetTabLocation(container.TabLocationLeading)

	s.win = s.app.NewWindow(SettingsTitle)
	s.win.SetIcon(AppIcon())
	s.win.SetContent(s.tabs)
	s.win.Resize(fyne.NewSize(640, 420))
	s.win.CenterOnScreen()
	s.win.SetCloseIntercept(s.closeRequested)
}

// closeRequested runs when the user closes the window.
func (s *SettingsWindow) closeRequested() {
	s.win.Hide()
	if s.onClose != nil {
		s.onClose()
	}
}

func page(title string, body ...fyne.CanvasObject) fyne.CanvasObject {
	heading := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	heading.SizeName = theme.SizeNameSubHeadingText
	return container.NewVBox(append([]fyne.CanvasObject{heading}, body...)...)
}

func paragraph(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	return l
}

func path(p string) *widget.Label {
	l := widget.NewLabelWithStyle(p, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	l.Wrapping = fyne.TextWrapBreak
	l.Selectable = true
	return l
}
