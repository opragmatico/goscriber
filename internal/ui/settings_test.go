package ui

import (
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

var info = SettingsInfo{Version: "v1.2.3", SettingsFile: "/home/me/settings.json", LogsDir: "/home/me/logs"}

// pageText joins the text of every label on a page.
func pageText(s *SettingsWindow, p Page) string {
	var out []string
	var walk func(fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		switch o := o.(type) {
		case *widget.Label:
			out = append(out, o.Text)
		case *fyne.Container:
			for _, c := range o.Objects {
				walk(c)
			}
		}
	}
	walk(s.tabs.Items[p].Content)
	return strings.Join(out, "\n")
}

func TestSettingsWindowIsCreatedOnFirstShow(t *testing.T) {
	a := test.NewTempApp(t)
	windows := len(a.Driver().AllWindows()) // the test driver starts with one
	s := NewSettingsWindow(a, info, nil)
	if s.Window() != nil || len(a.Driver().AllWindows()) != windows {
		t.Fatal("no window should exist before Show")
	}
	if s.Page() != PageGeneral {
		t.Errorf("page before Show = %v", s.Page())
	}

	s.Show(PageModels)
	w := s.Window()
	if w == nil || w.Title() != SettingsTitle {
		t.Fatalf("window = %v", w)
	}
	if s.Page() != PageModels {
		t.Errorf("page = %v, want Models", s.Page())
	}
	if w.Content() != s.tabs {
		t.Error("the pages should fill the window")
	}

	s.Show(PageAbout)
	if s.Window() != w || len(a.Driver().AllWindows()) != windows+1 {
		t.Error("Show should reuse the window")
	}
	if s.Page() != PageAbout {
		t.Errorf("page = %v, want About", s.Page())
	}
}

func TestSettingsWindowPages(t *testing.T) {
	s := NewSettingsWindow(test.NewTempApp(t), info, nil)
	s.Show(PageGeneral)

	var titles []string
	for _, it := range s.tabs.Items {
		titles = append(titles, it.Text)
	}
	if !slices.Equal(titles, []string{"General", "Models", "Permissions", "About"}) {
		t.Errorf("pages = %v", titles)
	}
	if !strings.Contains(pageText(s, PageGeneral), info.SettingsFile) {
		t.Error("General should show where settings are saved")
	}
	about := pageText(s, PageAbout)
	if !strings.Contains(about, "v1.2.3") || !strings.Contains(about, info.LogsDir) {
		t.Errorf("About = %q", about)
	}
}

func TestSettingsWindowModel(t *testing.T) {
	s := NewSettingsWindow(test.NewTempApp(t), info, nil)
	s.SetModel("Nemotron Streaming 3.5")
	s.Show(PageModels)
	if got := pageText(s, PageModels); !strings.Contains(got, "Selected model: Nemotron Streaming 3.5") {
		t.Errorf("Models = %q", got)
	}
	s.SetModel("")
	if got := pageText(s, PageModels); !strings.Contains(got, "No model is downloaded") {
		t.Errorf("Models = %q", got)
	}
}

func TestSettingsWindowCloseHides(t *testing.T) {
	closed := 0
	s := NewSettingsWindow(test.NewTempApp(t), info, func() { closed++ })
	s.Show(PageGeneral)
	w := s.Window()
	s.closeRequested()
	if closed != 1 {
		t.Errorf("onClose ran %d times", closed)
	}
	s.Show(PageModels)
	if s.Window() != w {
		t.Error("closing should keep the window for the next Show")
	}

	// A nil onClose only hides.
	s = NewSettingsWindow(test.NewTempApp(t), info, nil)
	s.Show(PageGeneral)
	s.closeRequested()
}

func TestSettingsWindowBanner(t *testing.T) {
	a := test.NewTempApp(t)
	s := NewSettingsWindow(a, info, nil)
	acted, dismissed := 0, 0
	b := &Banner{
		Error: true, Title: "No model downloaded", Message: "Download a speech model to start dictating.",
		ActionLabel: "Download a model", OnAction: func() { acted++ }, OnDismiss: func() { dismissed++ },
	}
	s.SetBanner(b) // before the window exists: shown when it opens
	if s.Visible() {
		t.Error("visible before Show")
	}
	s.Show(PageGeneral)
	if !s.Visible() || s.Banner() != b {
		t.Fatal("the window should be visible with the banner")
	}
	if s.Window().Content() == s.tabs {
		t.Fatal("the banner should sit above the pages")
	}

	var texts []string
	buttons := map[string]*widget.Button{}
	var walk func(fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		switch o := o.(type) {
		case *widget.Label:
			texts = append(texts, o.Text)
		case *widget.Button:
			buttons[o.Text] = o
		case *fyne.Container:
			for _, c := range o.Objects {
				walk(c)
			}
		}
	}
	walk(s.Window().Content().(*fyne.Container).Objects[1]) // the banner; Objects[0] is the pages
	if !slices.Contains(texts, "No model downloaded") || !slices.Contains(texts, "Download a speech model to start dictating.") {
		t.Errorf("banner text %q", texts)
	}
	test.Tap(buttons["Download a model"])
	test.Tap(buttons["Dismiss"])
	if acted != 1 || dismissed != 1 {
		t.Errorf("acted %d, dismissed %d", acted, dismissed)
	}

	s.SetBanner(nil)
	if s.Window().Content() != s.tabs {
		t.Error("without a banner the pages should fill the window again")
	}
	s.closeRequested()
	if s.Visible() {
		t.Error("visible after closing")
	}
}

func TestSettingsPermissionsPage(t *testing.T) {
	a := test.NewTempApp(t)
	s := NewSettingsWindow(a, info, nil)
	s.Show(PagePermissions)
	if got := pageText(s, PagePermissions); !strings.Contains(got, PermissionsNone) {
		t.Errorf("page %q, want the no-permissions note", got)
	}
	opened := 0
	s.SetPermissions([]PermissionRow{
		{Title: "Microphone", Why: "To hear you.", Status: "denied", OnOpen: func() { opened++ }},
		{Title: "Accessibility", Why: "To paste.", Status: "granted", OK: true},
	})
	got := pageText(s, PagePermissions)
	for _, want := range []string{"✗ Microphone: denied", "To hear you.", "✓ Accessibility: granted"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from %q", want, got)
		}
	}
	var buttons []*widget.Button
	var walk func(fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		switch o := o.(type) {
		case *widget.Button:
			buttons = append(buttons, o)
		case *fyne.Container:
			for _, c := range o.Objects {
				walk(c)
			}
		}
	}
	walk(s.tabs.Items[PagePermissions].Content)
	if len(buttons) != 1 {
		t.Fatalf("%d buttons, want one for the denied permission", len(buttons))
	}
	test.Tap(buttons[0])
	if opened != 1 {
		t.Error("the button should open System Settings")
	}
}

func TestSettingsLanguagePicker(t *testing.T) {
	a := test.NewTempApp(t)
	s := NewSettingsWindow(a, info, nil)
	var picked []string
	s.OnLanguage(func(code string) { picked = append(picked, code) })
	s.SetLanguage(LanguageSetting{
		Options:  []LanguageOption{{"auto", "Automatic"}, {"en-US", "English (US)"}, {"es-ES", "Spanish (Spain)"}},
		Selected: "auto",
	})
	s.Show(PageModels)
	if !s.languageBox.Visible() || s.languageSelect.Selected != "Automatic" || len(s.languageSelect.Options) != 3 {
		t.Fatalf("picker visible %v, selected %q, options %v", s.languageBox.Visible(), s.languageSelect.Selected, s.languageSelect.Options)
	}
	if len(picked) != 0 {
		t.Errorf("showing the setting reported a pick: %v", picked)
	}
	s.PickLanguage("es-ES")
	if !slices.Equal(picked, []string{"es-ES"}) || s.Language().Selected != "es-ES" {
		t.Errorf("picked %v, setting %+v", picked, s.Language())
	}
	if s.detectedLabel.Visible() {
		t.Error("no detected language yet")
	}
	s.SetDetected("Spanish (US)")
	if !s.detectedLabel.Visible() || !strings.Contains(pageText(s, PageModels), "Detected in the last dictation: Spanish (US)") {
		t.Errorf("page text %q", pageText(s, PageModels))
	}

	// A fixed language is shown but cannot change.
	s.SetLanguage(LanguageSetting{
		Options:  []LanguageOption{{"en", "English"}},
		Selected: "en",
		Locked:   true,
		Note:     "Parakeet TDT 0.6B v2 transcribes English only.",
	})
	if !s.languageSelect.Disabled() || s.languageSelect.Selected != "English" || !strings.Contains(pageText(s, PageModels), "English only") {
		t.Errorf("locked picker: disabled %v, selected %q", s.languageSelect.Disabled(), s.languageSelect.Selected)
	}
	s.PickLanguage("en")
	if len(picked) != 1 {
		t.Errorf("a locked picker reported a pick: %v", picked)
	}

	// No model, no picker.
	s.SetLanguage(LanguageSetting{})
	if s.languageBox.Visible() {
		t.Error("the picker shows without a model")
	}
}
