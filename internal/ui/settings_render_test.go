package ui

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/opragmatico/goscriber/internal/config"
)

// RenderDirEnv names a directory to write the settings pages to as PNGs,
// in Fyne's light and dark themes, to look at their layout:
//
//	GOSCRIBER_RENDER_DIR=/tmp/pages go test -run TestRenderSettingsPages ./internal/ui
const RenderDirEnv = "GOSCRIBER_RENDER_DIR"

func TestRenderSettingsPages(t *testing.T) {
	dir := os.Getenv(RenderDirEnv)
	if dir == "" {
		t.Skip(RenderDirEnv + " is not set")
	}
	for _, variant := range []struct {
		name  string
		theme fyne.Theme
	}{{"light", theme.LightTheme()}, {"dark", theme.DarkTheme()}} { //nolint:staticcheck // fixed variants, whatever the OS uses
		tw := newTestWindow(t, func(o *SettingsOptions) { o.ForceDebug = true })
		tw.app.Settings().SetTheme(variant.theme)
		tw.files.setReady(nemotron, "Q8_0")
		if err := tw.store.Update(func(s *config.Settings) {
			s.SelectedModel, s.CustomWords = nemotron, []string{"Kowalczyk", "NeMo-Speech.cpp", "Goscriber"}
		}); err != nil {
			t.Fatal(err)
		}
		tw.SetSettings(tw.store.Get())
		tw.SetDetected("Spanish (US)")
		tw.show(PageGeneral)
		tw.Window().Resize(fyne.NewSize(760, 2700)) // tall enough for a whole page
		for _, p := range tw.Pages() {
			tw.selectPage(p)
			f, err := os.Create(filepath.Join(dir, variant.name+"-"+p.String()+".png"))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, tw.Window().Canvas().Capture()); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
		// The delete confirmation, over the Models page.
		tw.selectPage(PageModels)
		tw.Window().Resize(fyne.NewSize(760, 560))
		tw.askDelete(tw.card(nemotron))
		f, err := os.Create(filepath.Join(dir, variant.name+"-confirm.png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tw.Window().Canvas().Capture()); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
