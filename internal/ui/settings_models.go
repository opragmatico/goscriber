package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/models"
)

// ModelFiles lists, downloads and deletes the model files. *models.Manager
// implements it.
type ModelFiles interface {
	Catalog() *models.Catalog
	List() []models.Status
	Download(ctx context.Context, id, quant string, progress chan<- models.Progress) error
	Delete(id string) error
}

// Texts of the Models page that tests look for.
const (
	ModelsNoneDownloaded = "No model is downloaded yet. Download one below to start dictating; each takes about 700 MB."
	LabelDownload        = "Download"
	LabelCancelDownload  = "Cancel download"
	LabelDeleteModel     = "Delete"
	LabelUseModel        = "Use this model"
	LabelModelInUse      = "In use"
)

// modelsState is what the Models page knows about the model files.
type modelsState struct {
	note      *widget.Label
	cards     []*modelCard
	statuses  []models.Status
	listed    bool                      // statuses has been read once
	listing   bool                      // a read is in progress
	again     bool                      // read again once it ends
	downloads map[string]*modelDownload // by model ID
	errs      map[string]string         // the last failure, by model ID
}

func (m *modelsState) init() {
	m.downloads = map[string]*modelDownload{}
	m.errs = map[string]string{}
}

// modelDownload is a download in flight.
type modelDownload struct {
	quant    string
	cancel   context.CancelFunc
	progress models.Progress
}

// modelCard is one model on the Models page.
type modelCard struct {
	model    *models.Model
	card     *widget.Card
	info     *widget.Label
	quant    *widget.Select // nil for a model with one file
	pick     string         // the quantisation the card shows
	picked   bool           // the user chose pick on the card
	status   *widget.Label
	progress *widget.ProgressBar
	download *widget.Button // also cancels a download in flight
	delete   *widget.Button
	use      *widget.Button
}

// catalog is the catalog the Models page shows.
func (w *SettingsWindow) catalog() *models.Catalog {
	if w.opts.Models != nil {
		return w.opts.Models.Catalog()
	}
	return models.Default()
}

func (w *SettingsWindow) modelsPage() fyne.CanvasObject {
	m := &w.models
	m.note = paragraph("")
	cards := container.NewVBox()
	for _, model := range w.catalog().ASR() {
		c := w.newModelCard(model)
		m.cards = append(m.cards, c)
		cards.Add(c.card)
	}
	// The cards show which model and quantisation are selected, and are
	// how the user changes them.
	refresh := func(config.Settings) { w.refreshModels() }
	w.bind("selected_model", anyField(func(s *config.Settings) *string { return &s.SelectedModel }), cards, refresh)
	w.bind("model_quant", anyField(func(s *config.Settings) *string { return &s.ModelQuant }), cards, refresh)
	return settingsPage("Models", m.note, cards, section("Language", w.languageView()))
}

func (w *SettingsWindow) newModelCard(model *models.Model) *modelCard {
	c := &modelCard{model: model, pick: model.DefaultQuant}
	c.info = paragraph("")
	c.status = widget.NewLabel("")
	c.status.Wrapping = fyne.TextWrapWord
	c.progress = widget.NewProgressBar()
	c.progress.Hide()
	c.download = widget.NewButtonWithIcon(LabelDownload, theme.DownloadIcon(), func() { w.downloadOrCancel(c) })
	c.download.Importance = widget.HighImportance
	c.delete = widget.NewButtonWithIcon(LabelDeleteModel, theme.DeleteIcon(), func() { w.askDelete(c) })
	c.use = widget.NewButtonWithIcon(LabelUseModel, theme.ConfirmIcon(), func() { w.useModel(c) })
	buttons := container.NewHBox(c.download, c.delete, layout.NewSpacer(), c.use)
	if len(model.Files) > 1 {
		c.quant = widget.NewSelect(model.Quants(), func(q string) {
			if q != "" && q != c.pick {
				c.pick, c.picked = q, true
				w.quantPicked(c)
			}
		})
		buttons.Objects = append([]fyne.CanvasObject{c.quant}, buttons.Objects...)
	}
	content := container.NewVBox(paragraph(model.Description), c.info, c.status, c.progress, buttons)
	c.card = widget.NewCard(model.Name, modelSubtitle(model, w.catalog().DefaultModel == model.ID), content)
	return c
}

// modelSubtitle says what a model can do, in a few words.
func modelSubtitle(m *models.Model, recommended bool) string {
	var parts []string
	if recommended {
		parts = append(parts, "Recommended")
	}
	if m.Capabilities.Streaming {
		parts = append(parts, "shows words as you speak")
	} else {
		parts = append(parts, "transcribes when you stop")
	}
	switch {
	case len(m.Languages) == 1:
		parts = append(parts, models.LanguageName(m.Languages[0])+" only")
	case m.Capabilities.LanguageSelection:
		parts = append(parts, fmt.Sprintf("%d languages", len(m.Languages)))
	case m.Capabilities.LanguageDetection:
		parts = append(parts, fmt.Sprintf("%d languages, detected", len(m.Languages)))
	}
	s := strings.Join(parts, " · ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// refreshModelFiles reads the model files again in the background: the
// first look at a file hashes it, which takes a moment.
func (w *SettingsWindow) refreshModelFiles() {
	m := &w.models
	if w.opts.Models == nil {
		m.listed = true
		w.refreshModels()
		return
	}
	if m.listing {
		m.again = true
		return
	}
	m.listing = true
	files := w.opts.Models
	go func() {
		statuses := files.List()
		w.do(func() {
			m.statuses, m.listed, m.listing = statuses, true, false
			w.refreshModels()
			if m.again {
				m.again = false
				w.refreshModelFiles()
			}
		})
	}()
}

// status is a file's status, by model and quantisation.
func (m *modelsState) status(id, quant string) (models.Status, bool) {
	for _, st := range m.statuses {
		if st.Model != nil && st.Model.ID == id && strings.EqualFold(st.File.Quant, quant) {
			return st, true
		}
	}
	return models.Status{}, false
}

// ready reports whether the model has a file ready in quant, or in any
// quantisation when quant is "".
func (m *modelsState) ready(id, quant string) bool {
	for _, st := range m.statuses {
		if st.Model != nil && st.Model.ID == id && st.State == models.Ready &&
			(quant == "" || strings.EqualFold(st.File.Quant, quant)) {
			return true
		}
	}
	return false
}

// onDisk reports whether Delete would remove anything of the model: a file
// in the models directory, whole, partial or damaged.
func (m *modelsState) onDisk(id string) bool {
	for _, st := range m.statuses {
		if st.Model == nil || st.Model.ID != id {
			continue
		}
		if (st.State == models.Ready && !st.FromHFCache) || st.State == models.Corrupt || st.Done > 0 && st.State != models.Ready {
			return true
		}
	}
	return false
}

// usedQuant is the quantisation a model runs in when selected: the
// model_quant setting when the model has it, else its default.
func usedQuant(m *models.Model, s config.Settings) string {
	if f, ok := m.File(s.ModelQuant); ok {
		return f.Quant
	}
	return m.DefaultQuant
}

// refreshModels brings the Models page in line with the settings, the
// files and the downloads in flight.
func (w *SettingsWindow) refreshModels() {
	m := &w.models
	if m.note == nil {
		return
	}
	s := w.cur
	anyReady := false
	for _, c := range m.cards {
		anyReady = anyReady || m.ready(c.model.ID, "")
	}
	selected, known := w.catalog().Model(s.SelectedModel)
	switch {
	case !m.listed:
		m.note.SetText("Checking the downloaded models…")
	case !anyReady:
		m.note.SetText(ModelsNoneDownloaded)
	case s.SelectedModel == "":
		m.note.SetText("Choose the model to dictate with: press " + LabelUseModel + " on a downloaded one.")
	case known && !m.ready(selected.ID, usedQuant(selected, s)):
		m.note.SetText(selected.Name + " is selected but not downloaded. Download it, or use another model.")
	default:
		m.note.SetText("")
	}
	m.note.Hidden = m.note.Text == ""
	m.note.Refresh()
	for _, c := range m.cards {
		w.refreshCard(c)
	}
}

func (w *SettingsWindow) refreshCard(c *modelCard) {
	m := &w.models
	s := w.cur
	id := c.model.ID
	dl := m.downloads[id]
	selected := s.SelectedModel == id

	// The card shows the quantisation in use, else the one downloading or
	// downloaded, unless the user chose one on the card.
	if !c.picked {
		switch {
		case selected:
			c.pick = usedQuant(c.model, s)
		case dl != nil:
			c.pick = dl.quant
		case !m.ready(id, c.pick):
			for _, q := range c.model.Quants() {
				if m.ready(id, q) {
					c.pick = q
					break
				}
			}
		}
	}
	if c.quant != nil {
		c.quant.SetSelected(c.pick)
		if dl != nil {
			c.quant.Disable()
		} else {
			c.quant.Enable()
		}
	}
	f, _ := c.model.File(c.pick)
	c.info.SetText(fmt.Sprintf("%s · %s · %s licence", models.FormatBytes(f.Size), f.Quant, c.model.License))

	st, hasStatus := m.status(id, c.pick)
	ready := hasStatus && st.State == models.Ready
	inUse := selected && ready && strings.EqualFold(usedQuant(c.model, s), c.pick)

	status := ""
	switch {
	case dl != nil && dl.progress.Verifying:
		status = "Checking the download…"
	case dl != nil:
		p := dl.progress
		status = "Downloading…"
		if p.Total > 0 {
			status = fmt.Sprintf("Downloading… %d %% (%s of %s)", p.Done*100/p.Total, models.FormatBytes(p.Done), models.FormatBytes(p.Total))
		}
	case m.errs[id] != "":
		status = m.errs[id]
	case !m.listed:
		status = "Checking…"
	case ready && st.FromHFCache:
		status = "Downloaded, in your Hugging Face cache"
	case ready:
		status = "Downloaded"
	case hasStatus && st.State == models.Corrupt:
		status = "The file is damaged. Download it again."
	case hasStatus && st.Done > 0:
		status = fmt.Sprintf("Download stopped at %d %%; downloading resumes it.", st.Percent())
	case w.opts.Models == nil:
		status = "Not downloaded. Use goscriber models pull to download it."
	default:
		status = "Not downloaded"
	}
	if inUse {
		status = "✓ " + status + " · in use"
	}
	c.status.SetText(status)

	if dl != nil {
		c.progress.Show()
		if dl.progress.Total > 0 {
			c.progress.SetValue(float64(dl.progress.Done) / float64(dl.progress.Total))
		}
	} else {
		c.progress.Hide()
	}

	switch {
	case dl != nil:
		c.download.SetText(LabelCancelDownload)
		c.download.SetIcon(theme.CancelIcon())
		c.download.Importance = widget.MediumImportance
		c.download.Show()
	case ready:
		c.download.Hide()
	default:
		c.download.SetText(LabelDownload)
		if hasStatus && st.Done > 0 && st.State != models.Corrupt {
			c.download.SetText("Resume download")
		}
		c.download.SetIcon(theme.DownloadIcon())
		c.download.Importance = widget.HighImportance
		c.download.Show()
	}
	// One download at a time: the first model's also fetches the VAD the
	// others share.
	if w.opts.Models == nil || !m.listed || dl == nil && len(m.downloads) > 0 {
		c.download.Disable()
	} else {
		c.download.Enable()
	}
	c.download.Refresh()

	if dl == nil && m.onDisk(id) {
		c.delete.Show()
	} else {
		c.delete.Hide()
	}

	switch {
	case inUse:
		c.use.SetText(LabelModelInUse)
		c.use.Disable()
	case ready && dl == nil:
		c.use.SetText(LabelUseModel)
		c.use.Enable()
	default:
		// A model can be selected only once it is downloaded.
		c.use.SetText(LabelUseModel)
		c.use.Disable()
	}
}

// quantPicked runs when the user picks another quantisation on a card. For
// the model in use, a downloaded quantisation is used at once.
func (w *SettingsWindow) quantPicked(c *modelCard) {
	if w.cur.SelectedModel == c.model.ID && w.models.ready(c.model.ID, c.pick) {
		setField(w, func(s *config.Settings) *string { return &s.ModelQuant }, c.pick)
	}
	w.refreshModels()
}

// useModel selects the card's model and quantisation, once downloaded.
func (w *SettingsWindow) useModel(c *modelCard) {
	if !w.models.ready(c.model.ID, c.pick) {
		return
	}
	id, quant := c.model.ID, c.pick
	w.change(func(s *config.Settings) { s.SelectedModel, s.ModelQuant = id, quant })
}

func (w *SettingsWindow) downloadOrCancel(c *modelCard) {
	if dl := w.models.downloads[c.model.ID]; dl != nil {
		dl.cancel()
		return
	}
	w.download(c.model.ID, c.pick)
}

// download fetches a model in the background, with its progress on the
// card. Once the first model is downloaded, or while the model selected is
// not downloaded, the new one is selected: the user just chose it.
func (w *SettingsWindow) download(id, quant string) {
	files := w.opts.Models
	if files == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	dl := &modelDownload{quant: quant, cancel: cancel}
	w.models.downloads[id] = dl
	delete(w.models.errs, id)
	w.refreshModels()

	progress := make(chan models.Progress, 4)
	go func() {
		stop := make(chan struct{})
		shown := make(chan struct{})
		go func() {
			defer close(shown)
			for {
				select {
				case p := <-progress:
					w.do(func() {
						if w.models.downloads[id] == dl {
							dl.progress = p
							w.refreshModels()
						}
					})
				case <-stop:
					return
				}
			}
		}()
		err := files.Download(ctx, id, quant, progress)
		close(stop)
		<-shown
		cancel()
		statuses := files.List()
		w.do(func() { w.downloaded(id, quant, err, statuses) })
	}()
}

// downloaded runs on the main thread when a download ends.
func (w *SettingsWindow) downloaded(id, quant string, err error, statuses []models.Status) {
	m := &w.models
	delete(m.downloads, id)
	m.statuses, m.listed = statuses, true
	switch {
	case errors.Is(err, context.Canceled):
	case err != nil:
		w.log().Warn("a model download failed", "model", id, "err", err)
		m.errs[id] = "Download failed: " + err.Error()
	default:
		s := w.cur
		sel, known := w.catalog().Model(s.SelectedModel)
		if s.SelectedModel == "" || !known || !m.ready(sel.ID, usedQuant(sel, s)) {
			w.change(func(s *config.Settings) { s.SelectedModel, s.ModelQuant = id, quant })
		}
	}
	w.refreshModels()
}

// askDelete deletes the card's model files once the user confirms.
func (w *SettingsWindow) askDelete(c *modelCard) {
	var size int64
	for _, st := range w.models.statuses {
		if st.Model != nil && st.Model.ID == c.model.ID && !st.FromHFCache && st.State != models.NotDownloaded {
			size += st.File.Size
		}
	}
	msg := "Its files are removed from this computer. You can download it again later."
	if size > 0 {
		msg = fmt.Sprintf("Its files (%s) are removed from this computer. You can download it again later.", models.FormatBytes(size))
	}
	w.confirm("Delete "+c.model.Name+"?", msg, LabelDeleteModel, func() { w.deleteModel(c.model.ID) })
}

// deleteModel deletes a model's files in the background. A model in use
// stops being selected, so dictation asks for one instead of failing.
func (w *SettingsWindow) deleteModel(id string) {
	files := w.opts.Models
	if files == nil {
		return
	}
	go func() {
		err := files.Delete(id)
		statuses := files.List()
		w.do(func() {
			m := &w.models
			m.statuses, m.listed = statuses, true
			if err != nil && !errors.Is(err, models.ErrNotDownloaded) {
				w.log().Warn("a model could not be deleted", "model", id, "err", err)
				m.errs[id] = "Could not delete it: " + err.Error()
			} else {
				delete(m.errs, id)
			}
			if w.cur.SelectedModel == id && !m.ready(id, "") {
				w.change(func(s *config.Settings) { s.SelectedModel = "" })
			}
			for _, c := range m.cards {
				if c.model.ID == id {
					c.picked = false
				}
			}
			w.refreshModels()
		})
	}()
}

// card returns a model's card, or nil.
func (w *SettingsWindow) card(id string) *modelCard {
	i := slices.IndexFunc(w.models.cards, func(c *modelCard) bool { return c.model.ID == id })
	if i < 0 {
		return nil
	}
	return w.models.cards[i]
}
