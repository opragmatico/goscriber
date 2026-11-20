package app

import (
	"context"
	"slices"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/models"
	"github.com/opragmatico/goscriber/internal/pipeline"
	"github.com/opragmatico/goscriber/internal/ui"
)

// releasesURL is where Check for Updates… leads: Goscriber has no updater of
// its own, so the releases page is where a newer version is found.
const releasesURL = "https://github.com/opragmatico/goscriber/releases"

// residentEngine is an engine that can free its model's memory on request and
// says when the model comes and goes, such as *autounload.Engine.
type residentEngine interface {
	Resident() bool
	UnloadNow(ctx context.Context) (bool, error)
	SetOnChange(f func())
}

// newTray installs the tray icon and its menu.
func (sh *shell) newTray(s config.Settings) *ui.Tray {
	return ui.NewTray(sh.desk, ui.TrayOptions{
		Actions: ui.TrayActions{
			StartDictation:     func() { sh.dictate(pipeline.ActionStart) },
			StopDictation:      func() { sh.dictate(pipeline.ActionStop) },
			Cancel:             func() { sh.dictate(pipeline.ActionCancel) },
			CopyLastTranscript: sh.copyLastTranscript,
			UseModel:           sh.useModel,
			OpenModels:         sh.openModels,
			UnloadModel:        sh.unloadModel,
			OpenSettings:       func() { sh.settings.Show(ui.PageGeneral) },
			CheckForUpdates:    func() { sh.openURL(releasesURL) },
			Quit:               sh.app.Quit,
		},
		Version: sh.opts.Version,
		Model:   sh.trayModel(s),
		Debug:   sh.debugMode(s),
	})
}

// followTray keeps the tray's model submenu and Unload Model in line with
// the model files and the engine, once the tray is installed.
func (sh *shell) followTray() {
	if sh.tray == nil {
		return
	}
	if e, ok := sh.svc.engine.(residentEngine); ok {
		// Only the newest news matters, and it is read when it runs.
		resident := &latest{do: sh.post}
		e.SetOnChange(func() { resident.post(func() { sh.tray.SetResident(e.Resident()) }) })
		sh.tray.SetResident(e.Resident())
	}
	if sh.svc.files != nil {
		sh.modelFilesChanged()
		sh.following.Go(sh.listModels)
	}
}

// catalog is the model catalog the tray names models from.
func (sh *shell) catalog() *models.Catalog {
	if sh.svc.files != nil {
		if c := sh.svc.files.Catalog(); c != nil {
			return c
		}
	}
	return models.Default()
}

// trayModel is the model the settings select, by its catalog name, or the
// zero TrayModel when none is selected.
func (sh *shell) trayModel(s config.Settings) ui.TrayModel {
	id := modelName(s)
	if id == "" {
		return ui.TrayModel{}
	}
	m := ui.TrayModel{ID: id, Name: id}
	if cm, ok := sh.catalog().Model(id); ok {
		m.Name = cm.Name
	}
	return m
}

// modelFilesChanged has the tray's model submenu listed again.
func (sh *shell) modelFilesChanged() {
	select {
	case sh.modelsChanged <- struct{}{}:
	default: // a listing is due already
	}
}

// listModels lists the downloaded models for the tray at launch and after
// each download or delete, until the app stops. Listing may hash a file seen
// for the first time, so it stays off the main thread.
func (sh *shell) listModels() {
	for {
		select {
		case <-sh.quit:
			return
		case <-sh.modelsChanged:
		}
		ms := readyModels(sh.svc.files)
		sh.post(func() { sh.tray.SetModels(ms) })
	}
}

// readyModels are the speech models with a downloaded file, in catalog
// order, each with the quantisation to use: its default one when that is
// downloaded, else the first downloaded.
func readyModels(files ui.ModelFiles) []ui.TrayModel {
	ready := map[string][]string{} // quantisations, by model ID
	for _, st := range files.List() {
		if st.State == models.Ready && st.Model.Kind == models.KindASR {
			ready[st.Model.ID] = append(ready[st.Model.ID], st.File.Quant)
		}
	}
	var out []ui.TrayModel
	for _, m := range files.Catalog().ASR() {
		quants := ready[m.ID]
		if len(quants) == 0 {
			continue
		}
		quant := quants[0]
		if slices.Contains(quants, m.DefaultQuant) {
			quant = m.DefaultQuant
		}
		out = append(out, ui.TrayModel{ID: m.ID, Name: m.Name, Quant: quant})
	}
	return out
}

// watchedFiles are the model files the settings window downloads and
// deletes, telling the shell after each change so the tray follows.
type watchedFiles struct {
	ui.ModelFiles
	changed func()
}

func (f watchedFiles) Download(ctx context.Context, id, quant string, progress chan<- models.Progress) error {
	defer f.changed()
	return f.ModelFiles.Download(ctx, id, quant, progress)
}

func (f watchedFiles) Delete(id string) error {
	defer f.changed()
	return f.ModelFiles.Delete(id)
}

// modelFiles are the model files for the settings window, or nil (not a nil
// interface inside watchedFiles) without any.
func (sh *shell) modelFiles() ui.ModelFiles {
	if sh.svc.files == nil {
		return nil
	}
	return watchedFiles{ModelFiles: sh.svc.files, changed: sh.modelFilesChanged}
}

// useModel selects a downloaded model from the tray; the settings follower
// then loads it as for a change on the Models page.
func (sh *shell) useModel(m ui.TrayModel) {
	err := sh.rt.Settings.Update(func(s *config.Settings) { s.SelectedModel, s.ModelQuant = m.ID, m.Quant })
	if err != nil {
		sh.rt.Log.Warn("could not switch the model", "model", m.ID, "err", err)
		return
	}
	sh.rt.Log.Info("model chosen from the tray", "model", m.ID, "quant", m.Quant)
}

// unloadModel frees the model's memory, as an idle unload would: the next
// dictation loads it again. The engine may be busy loading, so it runs in
// the background.
func (sh *shell) unloadModel() {
	e, ok := sh.svc.engine.(residentEngine)
	if !ok {
		return
	}
	ctx, cancel := sh.untilQuit()
	go func() {
		defer cancel()
		unloaded, err := e.UnloadNow(ctx)
		switch {
		case err != nil:
			sh.rt.Log.Warn("unloading the model failed", "err", err)
		case !unloaded:
			sh.rt.Log.Info("the model was not unloaded: it is in use or not loaded")
		}
	}()
}

// copyLastTranscript puts the newest transcript on the clipboard: the
// history's newest entry, so a deleted one is not copied, or without a
// history the last one dictated since launch.
func (sh *shell) copyLastTranscript() {
	if sh.history == nil {
		sh.copyTranscript(sh.lastTranscript)
		return
	}
	go func() {
		entries, err := sh.history.List(context.Background(), 1)
		if err != nil {
			sh.rt.Log.Warn("the last transcript could not be read from the history", "err", err)
			return
		}
		text := ""
		if len(entries) > 0 {
			text = entries[0].Text
		}
		sh.post(func() { sh.copyTranscript(text) })
	}()
}

func (sh *shell) copyTranscript(text string) {
	if text == "" {
		sh.rt.Log.Info("no transcript to copy yet")
		return
	}
	sh.app.Clipboard().SetContent(text)
}

// untilQuit is a context cancelled when the app stops or cancel is called.
func (sh *shell) untilQuit() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-sh.quit:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
