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
	if !slices.Equal(titles, []string{"General", "Models", "About"}) {
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
