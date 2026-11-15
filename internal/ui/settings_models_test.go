package ui

import (
	"errors"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/config"
)

const (
	nemotron   = "nemotron-3.5-asr-streaming-0.6b"
	parakeetV2 = "parakeet-tdt-0.6b-v2"
	parakeetV3 = "parakeet-tdt-0.6b-v3"
)

// cardButtons are the visible buttons of a model's card, by text.
func (tw *testWindow) cardButtons(t *testing.T, id string) map[string]*widget.Button {
	t.Helper()
	c := tw.card(id)
	if c == nil {
		t.Fatalf("no card for %s", id)
	}
	out := map[string]*widget.Button{}
	for _, b := range []*widget.Button{c.download, c.delete, c.use} {
		if b.Visible() {
			out[b.Text] = b
		}
	}
	return out
}

func (tw *testWindow) cardStatus(id string) string { return tw.card(id).status.Text }

// With no model downloaded the page says so and offers each model for
// download; none can be selected yet.
func TestModelsPageWithNoneDownloaded(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.show(PageModels)
	text := tw.pageText(PageModels)
	if !strings.Contains(text, ModelsNoneDownloaded) {
		t.Errorf("page %q", text)
	}
	for _, id := range []string{nemotron, parakeetV2, parakeetV3} {
		b := tw.cardButtons(t, id)
		if b[LabelDownload] == nil || b[LabelDownload].Disabled() {
			t.Errorf("%s: no Download button: %v", id, b)
		}
		if use := b[LabelUseModel]; use == nil || !use.Disabled() {
			t.Errorf("%s: a model that is not downloaded can be selected", id)
		}
		if b[LabelDeleteModel] != nil {
			t.Errorf("%s: nothing to delete", id)
		}
		if tw.cardStatus(id) != "Not downloaded" {
			t.Errorf("%s: %q", id, tw.cardStatus(id))
		}
	}
	for _, s := range []string{
		"Nemotron Streaming 3.5", "Recommended · shows words as you speak · 32 languages",
		"Parakeet TDT 0.6B v2", "Transcribes when you stop · English only", "742 MB · Q8_0 · OpenMDW-1.1 licence",
	} {
		if !strings.Contains(text, s) && !strings.Contains(cardTexts(tw), s) {
			t.Errorf("%q missing from the page", s)
		}
	}
	if tw.card(parakeetV2).quant == nil || tw.card(nemotron).quant != nil {
		t.Error("only a model with several quantisations offers a choice")
	}
}

// cardTexts joins every card's title, subtitle and info line.
func cardTexts(tw *testWindow) string {
	var out []string
	for _, c := range tw.models.cards {
		out = append(out, c.card.Title, c.card.Subtitle, c.info.Text)
	}
	return strings.Join(out, "\n")
}

// Downloading shows its progress, and the first model downloaded is
// selected; a second one is not, until the user picks it.
func TestModelsPageDownloadSelectsTheFirstModel(t *testing.T) {
	tw := newTestWindow(t, nil)
	gate := make(chan struct{})
	tw.files.setGate(gate)
	tw.show(PageModels)

	test.Tap(tw.cardButtons(t, nemotron)[LabelDownload])
	tw.q.until(t, "download progress", func() bool { return strings.HasPrefix(tw.cardStatus(nemotron), "Downloading… 25 %") })
	c := tw.card(nemotron)
	if !c.progress.Visible() || c.progress.Value != 0.25 {
		t.Errorf("progress bar visible %v at %v", c.progress.Visible(), c.progress.Value)
	}
	if b := tw.cardButtons(t, nemotron); b[LabelCancelDownload] == nil || b[LabelUseModel] == nil || !b[LabelUseModel].Disabled() {
		t.Errorf("buttons while downloading: %v", b)
	}
	if b := tw.cardButtons(t, parakeetV2)[LabelDownload]; b == nil || !b.Disabled() {
		t.Error("another download can start while one is in flight")
	}
	close(gate)
	tw.q.until(t, "the download to end", func() bool { return tw.store.Get().SelectedModel == nemotron })
	tw.q.drain()
	if s := tw.store.Get(); s.ModelQuant != "Q8_0" {
		t.Errorf("quant %q", s.ModelQuant)
	}
	if got := tw.cardStatus(nemotron); got != "✓ Downloaded · in use" {
		t.Errorf("status %q", got)
	}
	if b := tw.cardButtons(t, nemotron); b[LabelModelInUse] == nil || !b[LabelModelInUse].Disabled() || b[LabelDeleteModel] == nil || b[LabelDownload] != nil {
		t.Errorf("buttons once downloaded: %v", b)
	}
	if strings.Contains(tw.pageText(PageModels), ModelsNoneDownloaded) {
		t.Error("the page still says no model is downloaded")
	}
	if tw.Language().Selected != "auto" || !tw.languageBox.Visible() {
		t.Error("the language picker should follow the new model")
	}

	// A second model downloads without taking over...
	tw.files.setGate(nil)
	test.Tap(tw.cardButtons(t, parakeetV3)[LabelDownload])
	tw.q.until(t, "the second download", func() bool { return tw.cardStatus(parakeetV3) == "Downloaded" })
	if got := tw.store.Get().SelectedModel; got != nemotron {
		t.Errorf("the second download selected %s", got)
	}
	// ...until the user picks it.
	test.Tap(tw.cardButtons(t, parakeetV3)[LabelUseModel])
	if got := tw.reopen(t).SelectedModel; got != parakeetV3 {
		t.Errorf("selected %s after Use", got)
	}
	if tw.cardStatus(nemotron) != "Downloaded" || tw.cardStatus(parakeetV3) != "✓ Downloaded · in use" {
		t.Errorf("statuses %q, %q", tw.cardStatus(nemotron), tw.cardStatus(parakeetV3))
	}
}

func TestModelsPageCancelAndFailedDownload(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.files.setGate(make(chan struct{}))
	tw.show(PageModels)
	test.Tap(tw.cardButtons(t, parakeetV2)[LabelDownload])
	tw.q.until(t, "the download to start", func() bool { return tw.cardButtons(t, parakeetV2)[LabelCancelDownload] != nil })
	test.Tap(tw.cardButtons(t, parakeetV2)[LabelCancelDownload])
	tw.q.until(t, "the download to stop", func() bool { return tw.cardButtons(t, parakeetV2)[LabelDownload] != nil })
	if tw.cardStatus(parakeetV2) != "Not downloaded" || tw.store.Get().SelectedModel != "" {
		t.Errorf("after cancelling: %q, selected %q", tw.cardStatus(parakeetV2), tw.store.Get().SelectedModel)
	}

	tw.files.setGate(nil)
	tw.files.setFail(errors.New("no space left"))
	test.Tap(tw.cardButtons(t, parakeetV2)[LabelDownload])
	tw.q.until(t, "the download to fail", func() bool { return strings.Contains(tw.cardStatus(parakeetV2), "no space left") })
	if tw.store.Get().SelectedModel != "" {
		t.Error("a failed download selected the model")
	}
}

// Parakeet TDT v2 comes in two quantisations: the card downloads and uses
// the one picked, and switches the model in use to another downloaded one.
func TestModelsPageQuantisation(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.files.setReady(parakeetV2, "Q8_0")
	if err := tw.store.Update(func(s *config.Settings) { s.SelectedModel = parakeetV2 }); err != nil {
		t.Fatal(err)
	}
	tw.SetSettings(tw.store.Get())
	tw.show(PageModels)
	c := tw.card(parakeetV2)
	if c.quant.Selected != "Q8_0" || tw.cardStatus(parakeetV2) != "✓ Downloaded · in use" {
		t.Fatalf("quant %q, status %q", c.quant.Selected, tw.cardStatus(parakeetV2))
	}
	c.quant.SetSelected("F16")
	if tw.cardStatus(parakeetV2) != "Not downloaded" || !strings.Contains(c.info.Text, "1.3 GB · F16") {
		t.Errorf("F16: status %q, info %q", tw.cardStatus(parakeetV2), c.info.Text)
	}
	if got := tw.store.Get().ModelQuant; got != "Q8_0" {
		t.Errorf("picking a quantisation not downloaded changed the setting to %q", got)
	}
	test.Tap(tw.cardButtons(t, parakeetV2)[LabelDownload])
	tw.q.until(t, "F16 to download", func() bool { return strings.Contains(tw.cardStatus(parakeetV2), "Downloaded") })
	test.Tap(tw.cardButtons(t, parakeetV2)[LabelUseModel])
	if s := tw.reopen(t); s.SelectedModel != parakeetV2 || s.ModelQuant != "F16" {
		t.Errorf("using F16 saved %s %s", s.SelectedModel, s.ModelQuant)
	}
	c.quant.SetSelected("Q8_0") // downloaded: the model in use switches at once
	if got := tw.store.Get().ModelQuant; got != "Q8_0" {
		t.Errorf("quant %q after picking the downloaded Q8_0", got)
	}
}

// Deleting asks first, and deleting the model in use unselects it.
func TestModelsPageDelete(t *testing.T) {
	tw := newTestWindow(t, nil)
	var asked []string
	tw.files.setReady(nemotron, "Q8_0")
	if err := tw.store.Update(func(s *config.Settings) { s.SelectedModel = nemotron }); err != nil {
		t.Fatal(err)
	}
	tw.SetSettings(tw.store.Get())
	tw.show(PageModels)
	var confirm func()
	tw.confirm = func(title, message, action string, ok func()) {
		asked = append(asked, title+" "+message+" ["+action+"]")
		confirm = ok
	}
	test.Tap(tw.cardButtons(t, nemotron)[LabelDeleteModel])
	if len(asked) != 1 || !strings.Contains(asked[0], "Delete Nemotron Streaming 3.5?") || !strings.Contains(asked[0], "742 MB") {
		t.Fatalf("asked %q", asked)
	}
	if len(tw.files.deletedIDs()) != 0 {
		t.Fatal("deleted before the user confirmed")
	}
	confirm()
	tw.q.until(t, "the delete", func() bool { return tw.store.Get().SelectedModel == "" })
	tw.q.drain()
	if len(tw.files.deletedIDs()) != 1 || tw.cardStatus(nemotron) != "Not downloaded" {
		t.Errorf("deleted %v, status %q", tw.files.deleted, tw.cardStatus(nemotron))
	}
	if !strings.Contains(tw.pageText(PageModels), ModelsNoneDownloaded) {
		t.Error("the page should say no model is downloaded again")
	}
}

// The real confirmation pop-up deletes only when its button is pressed.
func TestDeleteConfirmationPopUp(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.files.setReady(parakeetV3, "Q8_0")
	tw.show(PageModels)
	test.Tap(tw.cardButtons(t, parakeetV3)[LabelDeleteModel])
	overlay := tw.Window().Canvas().Overlays().Top()
	if overlay == nil {
		t.Fatal("no confirmation")
	}
	buttons := map[string]*widget.Button{}
	for _, o := range test.LaidOutObjects(overlay) {
		if b, ok := o.(*widget.Button); ok {
			buttons[b.Text] = b
		}
	}
	test.Tap(buttons["Cancel"])
	tw.q.drain()
	if len(tw.files.deletedIDs()) != 0 || tw.Window().Canvas().Overlays().Top() != nil {
		t.Fatalf("Cancel deleted %v", tw.files.deletedIDs())
	}
	test.Tap(tw.cardButtons(t, parakeetV3)[LabelDeleteModel])
	for _, o := range test.LaidOutObjects(tw.Window().Canvas().Overlays().Top()) {
		if b, ok := o.(*widget.Button); ok && b.Text == LabelDeleteModel {
			test.Tap(b)
		}
	}
	tw.q.until(t, "the delete", func() bool { return len(tw.files.deletedIDs()) == 1 })
}

// A model selected in the settings file but not downloaded is pointed out.
func TestModelsPageSelectedButMissing(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.files.setReady(parakeetV3, "Q8_0")
	if err := tw.store.Update(func(s *config.Settings) { s.SelectedModel = nemotron }); err != nil {
		t.Fatal(err)
	}
	tw.SetSettings(tw.store.Get())
	tw.show(PageModels)
	if !strings.Contains(tw.pageText(PageModels), "Nemotron Streaming 3.5 is selected but not downloaded") {
		t.Errorf("page %q", tw.pageText(PageModels))
	}
	if b := tw.cardButtons(t, nemotron)[LabelUseModel]; b == nil || !b.Disabled() {
		t.Error("the missing model must not look usable")
	}
}
