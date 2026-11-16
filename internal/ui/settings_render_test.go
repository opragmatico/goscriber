package ui

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/history"
	"github.com/opragmatico/goscriber/internal/session"
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
		tw := newTestWindow(t, func(o *SettingsOptions) {
			o.ForceDebug = true
			o.HistoryActions.TranscribeFile = func(ctx context.Context, _ string, _ func(done, total time.Duration)) (history.Entry, error) {
				<-ctx.Done()
				return history.Entry{}, ctx.Err()
			}
		})
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
		// A file being transcribed, with another waiting, on the History page.
		tw.selectPage(PageHistory)
		f := &tw.history.files
		f.now, f.done, f.total, f.queue = "/audio/interview.flac", 200*time.Second, 600*time.Second, []string{"/audio/next.wav"}
		tw.refreshFiles()
		capture(t, tw, filepath.Join(dir, variant.name+"-history-file.png"))
		f.now, f.queue = "", nil
		tw.refreshFiles()

		// The delete confirmation, over the Models page.
		tw.selectPage(PageModels)
		tw.Window().Resize(fyne.NewSize(760, 560))
		tw.askDelete(tw.card(nemotron))
		capture(t, tw, filepath.Join(dir, variant.name+"-confirm.png"))
		tw.Window().Canvas().Overlays().Top().Hide()

		// Each step of onboarding, at the window's own size, with Parakeet
		// half downloaded on the model step.
		tw.SetPermissions([]PermissionRow{
			{Title: "Microphone", Why: "Goscriber records your voice while you hold the shortcut.", Status: "not asked yet", ActionLabel: "Allow…", OnOpen: func() {}},
			{Title: "Accessibility", Why: "Goscriber hears the global shortcut and pastes the transcript.", Status: "granted", OK: true},
		})
		for step := StepWelcome; step < stepCount; step++ {
			tw.showOnboarding(step)
			if step == StepModel {
				tw.models.downloads[parakeetV2] = &modelDownload{quant: "Q8_0", cancel: func() {}}
				tw.models.downloads[parakeetV2].progress.Done, tw.models.downloads[parakeetV2].progress.Total = 350e6, 700e6
				tw.refreshModels()
			}
			capture(t, tw, filepath.Join(dir, variant.name+"-onboarding-"+step.String()+".png"))
			delete(tw.models.downloads, parakeetV2)
		}
	}

	// Linux on Wayland, with a CPU-only engine: the pages and the
	// onboarding step that differ there.
	tw := newTestWindow(t, func(o *SettingsOptions) {
		o.Info.OS, o.Info.Session, o.Info.EngineGPU = "linux", session.Wayland, ""
		o.Info.TypingTools = []config.TypingTool{config.TypingWtype, config.TypingYdotool}
	})
	tw.app.Settings().SetTheme(theme.LightTheme()) //nolint:staticcheck // a fixed variant, whatever the OS uses
	tw.show(PageGeneral)
	tw.Window().Resize(fyne.NewSize(760, 2700))
	for _, p := range []Page{PageGeneral, PageAdvanced, PagePermissions, PageAbout} {
		tw.selectPage(p)
		capture(t, tw, filepath.Join(dir, "linux-wayland-"+p.String()+".png"))
	}
	tw.showOnboarding(StepPermissions)
	capture(t, tw, filepath.Join(dir, "linux-wayland-onboarding-permissions.png"))

	// The same once the desktop's global shortcuts portal bound the keys.
	tw.endOnboarding(true)
	tw.SetDesktopShortcuts(DesktopShortcuts{State: PortalBound, Transcribe: "Ctrl+Alt+Space", CanChange: true})
	tw.show(PageGeneral)
	tw.Window().Resize(fyne.NewSize(760, 2700))
	capture(t, tw, filepath.Join(dir, "linux-wayland-portal-"+PageGeneral.String()+".png"))
	tw.showOnboarding(StepTry)
	capture(t, tw, filepath.Join(dir, "linux-wayland-portal-onboarding-try.png"))
}

// capture writes the window as a PNG.
func capture(t *testing.T, tw *testWindow, file string) {
	t.Helper()
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tw.Window().Canvas().Capture()); err != nil {
		t.Fatal(err)
	}
}
