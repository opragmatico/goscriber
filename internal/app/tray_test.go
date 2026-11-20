package app

import (
	"context"
	"slices"
	"sync"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine/autounload"
	"github.com/opragmatico/goscriber/internal/models"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/ui"
)

// withServices has the shells the test starts use the test services as
// changed by change.
func withServices(t *testing.T, change func(*services)) {
	t.Helper()
	old := newServices
	newServices = func(rt *Runtime, opts Options, n *notice.Hub) services {
		s := old(rt, opts, n)
		change(&s)
		return s
	}
	t.Cleanup(func() { newServices = old })
}

// readyFiles are model files of the built-in catalog that are all
// downloaded, but for the ones deleted.
type readyFiles struct {
	mu      sync.Mutex
	ready   map[string][]string // quantisations, by model ID
	deleted []string
}

func (f *readyFiles) Catalog() *models.Catalog { return models.Default() }

func (f *readyFiles) List() []models.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.Status
	cat := models.Default()
	for i := range cat.Models {
		m := &cat.Models[i]
		for _, file := range m.Files {
			st := models.Status{Model: m, File: file}
			if slices.Contains(f.ready[m.ID], file.Quant) {
				st.State = models.Ready
			}
			out = append(out, st)
		}
	}
	return out
}

func (f *readyFiles) Download(context.Context, string, string, chan<- models.Progress) error {
	return nil
}

func (f *readyFiles) Delete(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.ready, id)
	f.deleted = append(f.deleted, id)
	return nil
}

func submenu(t *testing.T, m *fyne.Menu, label string) *fyne.Menu {
	t.Helper()
	sub := item(t, m, label).ChildMenu
	if sub == nil {
		t.Fatalf("%q has no submenu", label)
	}
	return sub
}

func menuLabels(m *fyne.Menu) []string {
	var out []string
	for _, it := range m.Items {
		if !it.IsSeparator {
			out = append(out, it.Label)
		}
	}
	return out
}

// The model submenu lists the downloaded models, switches between them, and
// follows a delete on the Models page.
func TestTrayModelSubmenu(t *testing.T) {
	isolate(t)
	files := &readyFiles{ready: map[string][]string{
		"parakeet-tdt-0.6b-v2":            {"F16"},
		"nemotron-3.5-asr-streaming-0.6b": {"Q8_0"},
		"silero-vad":                      {"F32"}, // not a speech model
	}}
	withServices(t, func(s *services) { s.files = files })
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShellWith(t, a, func(s *config.Settings) { s.SelectedModel = "parakeet-tdt-0.6b-v2" })

	want := []string{"Nemotron Streaming 3.5", "Parakeet TDT 0.6B v2", ui.LabelManageModels}
	q.waitUI(t, "the downloaded models", func() bool {
		return slices.Equal(menuLabels(submenu(t, a.menu, "Parakeet TDT 0.6B v2")), want)
	})
	sub := submenu(t, a.menu, "Parakeet TDT 0.6B v2")
	if !item(t, sub, "Parakeet TDT 0.6B v2").Checked {
		t.Error("the selected model should be checked")
	}

	// Parakeet v2 has only F16 on disk, so choosing it again selects that.
	item(t, sub, "Nemotron Streaming 3.5").Action()
	q.runNext(t)
	if s := sh.rt.Settings.Get(); s.SelectedModel != "nemotron-3.5-asr-streaming-0.6b" || s.ModelQuant != "Q8_0" {
		t.Fatalf("selected %q %q, want Nemotron", s.SelectedModel, s.ModelQuant)
	}
	item(t, submenu(t, a.menu, "Nemotron Streaming 3.5"), "Parakeet TDT 0.6B v2").Action()
	q.runNext(t)
	if s := sh.rt.Settings.Get(); s.SelectedModel != "parakeet-tdt-0.6b-v2" || s.ModelQuant != "F16" {
		t.Fatalf("selected %q %q, want Parakeet's downloaded F16", s.SelectedModel, s.ModelQuant)
	}

	// A delete through the settings window lists the models again.
	if err := sh.modelFiles().Delete("nemotron-3.5-asr-streaming-0.6b"); err != nil {
		t.Fatal(err)
	}
	q.waitUI(t, "the deleted model to leave the submenu", func() bool {
		return slices.Equal(menuLabels(submenu(t, a.menu, "Parakeet TDT 0.6B v2")), []string{"Parakeet TDT 0.6B v2", ui.LabelManageModels})
	})
}

// Copy Last Transcript copies the history's newest transcript.
func TestTrayCopiesTheLastTranscript(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	if sh.history == nil {
		t.Fatal("the history did not open")
	}

	clip := a.Clipboard()
	clip.SetContent("before")
	item(t, a.menu, ui.LabelCopyLastTranscript).Action()
	q.drain()
	if got := clip.Content(); got != "before" {
		t.Errorf("clipboard %q; with no transcript yet nothing should be copied", got)
	}

	item(t, a.menu, ui.LabelStartDictation).Action()
	q.waitUI(t, "the recording icon", func() bool { return a.trayIcon == ui.TrayIcon(ui.TrayRecording) })
	item(t, a.menu, ui.LabelStopDictation).Action()
	q.waitUI(t, "the dictation to be saved", func() bool {
		n, _ := sh.history.Count(context.Background())
		return n == 1
	})
	item(t, a.menu, ui.LabelCopyLastTranscript).Action()
	q.waitUI(t, "the transcript on the clipboard", func() bool { return clip.Content() == "hello from the test" })
}

// Unload Model frees the model, which stays selected, and is offered only
// while a model is in memory.
func TestTrayUnloadsTheModel(t *testing.T) {
	isolate(t)
	var eng *autounload.Engine
	withServices(t, func(s *services) {
		eng = autounload.New(s.engine, autounload.Never, nil)
		s.engine = eng
	})
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShellWith(t, a, func(s *config.Settings) { s.SelectedModel = "parakeet-tdt-0.6b-v2" })

	// The engine's news and the coordinator's come by different ways, so
	// the load is over once the tray has shown it ending.
	q.waitUI(t, "the model to load", func() bool { return sh.tray.Loading() != "" })
	q.waitUI(t, "Unload Model to be offered", func() bool {
		return sh.tray.Loading() == "" && !item(t, a.menu, ui.LabelUnloadModel).Disabled
	})
	item(t, a.menu, ui.LabelUnloadModel).Action()
	q.waitUI(t, "the model to unload", func() bool {
		return !eng.Resident() && item(t, a.menu, ui.LabelUnloadModel).Disabled
	})
	if _, selected := eng.Model(); !selected {
		t.Error("the model should stay selected, to load again at the next dictation")
	}
}

// The menu starts with the version, and Check for Updates… opens the
// releases page.
func TestTrayVersionAndUpdates(t *testing.T) {
	isolate(t)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	newTestShell(t, a, Options{Version: "v9.9.9"})

	if first := a.menu.Items[0]; first.Label != "Goscriber v9.9.9" || !first.Disabled {
		t.Errorf("first item %q (disabled %v), want the version", first.Label, first.Disabled)
	}
	item(t, a.menu, ui.LabelCheckForUpdates).Action()
	if !slices.Equal(a.opened, []string{releasesURL}) {
		t.Errorf("opened %v, want the releases page", a.opened)
	}
}
