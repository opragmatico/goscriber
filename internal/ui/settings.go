package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Page is a page of the settings window.
type Page int

// Settings pages, in sidebar order.
const (
	PageGeneral Page = iota
	PageModels
	PagePermissions
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
	banner  *Banner
	visible bool

	win         fyne.Window
	tabs        *container.AppTabs
	modelLabel  *widget.Label
	permissions []PermissionRow
	permBox     *fyne.Container

	language       LanguageSetting
	detected       string
	onLanguage     func(code string)
	languageBox    *fyne.Container
	languageSelect *widget.Select
	languageNote   *widget.Label
	detectedLabel  *widget.Label
	settingSelect  bool // the window itself is changing the selection
}

// LanguageOption is a language the user can pick: its code, such as
// "es-ES", and its readable name.
type LanguageOption struct {
	Code, Name string
}

// LanguageSetting is what the Models page's language picker shows.
type LanguageSetting struct {
	// Options are the languages to pick from. None hides the picker.
	Options []LanguageOption
	// Selected is the code of the language picked.
	Selected string
	// Locked shows the language without letting it change: the model's
	// language is fixed. Note says why.
	Locked bool
	Note   string
}

// PermissionRow is one permission on the Permissions page.
type PermissionRow struct {
	Title  string
	Why    string
	Status string // such as "granted" or "denied"
	// OK is true when the permission lets Goscriber work.
	OK bool
	// OnOpen opens System Settings at the permission; nil shows no button.
	OnOpen func()
}

// PermissionsNone is what the Permissions page says on a system without
// permissions to grant.
const PermissionsNone = "Goscriber needs no special permissions on this system."

// Banner is a problem shown across the top of the settings window, with a
// button that fixes it and one that dismisses it.
type Banner struct {
	Error       bool // an error (red) rather than a warning (amber)
	Title       string
	Message     string
	ActionLabel string // no action button when empty
	OnAction    func()
	OnDismiss   func()
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
	s.visible = true
}

// Visible reports whether the window is on screen.
func (s *SettingsWindow) Visible() bool { return s.visible }

// Banner is the banner shown, or nil.
func (s *SettingsWindow) Banner() *Banner { return s.banner }

// SetBanner shows b across the top of the window, or removes the banner
// with nil.
func (s *SettingsWindow) SetBanner(b *Banner) {
	s.banner = b
	if s.win != nil {
		s.layout()
	}
}

// layout puts the banner, if any, above the pages.
func (s *SettingsWindow) layout() {
	if s.banner == nil {
		s.win.SetContent(s.tabs)
		return
	}
	s.win.SetContent(container.NewBorder(bannerView(s.banner), nil, nil, nil, s.tabs))
}

func bannerView(b *Banner) fyne.CanvasObject {
	title := widget.NewLabelWithStyle(b.Title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	message := paragraph(b.Message)
	buttons := container.NewHBox()
	if b.ActionLabel != "" {
		action := widget.NewButton(b.ActionLabel, b.OnAction)
		action.Importance = widget.HighImportance
		buttons.Add(action)
	}
	buttons.Add(widget.NewButton("Dismiss", b.OnDismiss))

	color := theme.Color(theme.ColorNameWarning)
	if b.Error {
		color = theme.Color(theme.ColorNameError)
	}
	stripe := canvas.NewRectangle(color)
	stripe.SetMinSize(fyne.NewSize(4, 0))
	body := container.NewVBox(title, message, container.NewHBox(layout.NewSpacer(), buttons))
	return container.NewPadded(container.NewBorder(nil, nil, stripe, nil, body))
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

// Language is what the language picker shows.
func (s *SettingsWindow) Language() LanguageSetting { return s.language }

// SetLanguage sets what the language picker shows.
func (s *SettingsWindow) SetLanguage(l LanguageSetting) {
	s.language = l
	s.refreshLanguage()
}

// OnLanguage sets what runs when the user picks a language, on the main
// thread, with the language's code.
func (s *SettingsWindow) OnLanguage(f func(code string)) { s.onLanguage = f }

// Detected is the readable name of the language the last dictation was
// detected in, or "".
func (s *SettingsWindow) Detected() string { return s.detected }

// SetDetected shows the language the last dictation was detected in, by its
// readable name; "" shows none.
func (s *SettingsWindow) SetDetected(name string) {
	s.detected = name
	s.refreshLanguage()
}

// PickLanguage picks a language by code as the user would, as if chosen in
// the picker.
func (s *SettingsWindow) PickLanguage(code string) {
	for _, o := range s.language.Options {
		if o.Code == code && s.languageSelect != nil && !s.language.Locked {
			s.languageSelect.SetSelected(o.Name)
		}
	}
}

func (s *SettingsWindow) languageView() fyne.CanvasObject {
	s.languageSelect = widget.NewSelect(nil, s.languagePicked)
	s.languageNote = paragraph("")
	s.detectedLabel = paragraph("")
	row := container.NewBorder(nil, nil, widget.NewLabel("Language"), nil, s.languageSelect)
	s.languageBox = container.NewVBox(row, s.languageNote, s.detectedLabel)
	s.refreshLanguage()
	return s.languageBox
}

// refreshLanguage brings the picker in line with the language setting.
func (s *SettingsWindow) refreshLanguage() {
	if s.languageBox == nil {
		return
	}
	l := s.language
	if len(l.Options) == 0 {
		s.languageBox.Hide()
		return
	}
	names := make([]string, len(l.Options))
	selected := ""
	for i, o := range l.Options {
		names[i] = o.Name
		if o.Code == l.Selected {
			selected = o.Name
		}
	}
	s.settingSelect = true
	s.languageSelect.SetOptions(names)
	s.languageSelect.SetSelected(selected)
	s.settingSelect = false
	if l.Locked {
		s.languageSelect.Disable()
	} else {
		s.languageSelect.Enable()
	}
	s.languageNote.SetText(l.Note)
	s.languageNote.Hidden = l.Note == ""
	s.detectedLabel.SetText("Detected in the last dictation: " + s.detected)
	s.detectedLabel.Hidden = s.detected == ""
	s.languageBox.Show()
	s.languageBox.Refresh()
}

func (s *SettingsWindow) languagePicked(name string) {
	if s.settingSelect || s.language.Locked {
		return
	}
	for _, o := range s.language.Options {
		if o.Name == name && o.Code != s.language.Selected {
			s.language.Selected = o.Code
			if s.onLanguage != nil {
				s.onLanguage(o.Code)
			}
		}
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
		container.NewTabItem("Models", page("Models", s.modelLabel, s.languageView())),
		container.NewTabItem("Permissions", page("Permissions", s.permissionsView())),
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
	s.layout()
	s.win.Resize(fyne.NewSize(640, 420))
	s.win.CenterOnScreen()
	s.win.SetCloseIntercept(s.closeRequested)
}

// Permissions are the rows of the Permissions page.
func (s *SettingsWindow) Permissions() []PermissionRow { return s.permissions }

// SetPermissions shows rows on the Permissions page; none says no
// permission is needed.
func (s *SettingsWindow) SetPermissions(rows []PermissionRow) {
	s.permissions = rows
	if s.permBox != nil {
		s.permBox.Objects = s.permissionRows()
		s.permBox.Refresh()
	}
}

func (s *SettingsWindow) permissionsView() fyne.CanvasObject {
	s.permBox = container.NewVBox(s.permissionRows()...)
	return s.permBox
}

func (s *SettingsWindow) permissionRows() []fyne.CanvasObject {
	if len(s.permissions) == 0 {
		return []fyne.CanvasObject{paragraph(PermissionsNone)}
	}
	var out []fyne.CanvasObject
	for _, r := range s.permissions {
		mark := "✓"
		if !r.OK {
			mark = "✗"
		}
		head := widget.NewLabelWithStyle(mark+" "+r.Title+": "+r.Status, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		row := container.NewVBox(head, paragraph(r.Why))
		if r.OnOpen != nil {
			row.Add(container.NewHBox(widget.NewButton("Open System Settings", r.OnOpen)))
		}
		out = append(out, row)
	}
	return out
}

// closeRequested runs when the user closes the window.
func (s *SettingsWindow) closeRequested() {
	s.win.Hide()
	s.visible = false
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
