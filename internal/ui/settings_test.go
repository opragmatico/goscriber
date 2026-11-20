package ui

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/logging"
	"github.com/opragmatico/goscriber/internal/models"
)

var info = SettingsInfo{Version: "v1.2.3", EngineVersion: "0.3.0", SettingsFile: "/home/me/settings.json", LogsDir: "/home/me/logs"}

// uiQueue stands in for fyne.Do: background work posts UI updates, and the
// test runs them on its own goroutine, as the main thread would.
type uiQueue chan func()

func (q uiQueue) do(f func()) { q <- f }

// drain runs the UI updates that arrive until none has for a while.
func (q uiQueue) drain() {
	for {
		select {
		case f := <-q:
			f()
		case <-time.After(50 * time.Millisecond):
			return
		}
	}
}

// until runs UI updates until cond holds.
func (q uiQueue) until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !cond() {
		select {
		case f := <-q:
			f()
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// testWindow is a settings window over a settings file in a temporary
// directory, fake model files and two microphones.
type testWindow struct {
	*SettingsWindow
	store *config.Store
	files *fakeFiles
	q     uiQueue
	// recording counts OnRecordShortcut calls: true, then false.
	recording []bool
}

func newTestWindow(t *testing.T, edit func(*SettingsOptions)) *testWindow {
	t.Helper()
	store, _, err := config.Open(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	tw := &testWindow{store: store, files: newFakeFiles(), q: make(uiQueue, 64)}
	opts := SettingsOptions{
		Info:   info,
		Store:  store,
		Models: tw.files,
		Devices: func() ([]audio.Device, []audio.Device, error) {
			return []audio.Device{{Name: "MacBook Pro Microphone", Default: true}, {Name: "USB Mic"}},
				[]audio.Device{{Name: "MacBook Pro Speakers", Default: true}}, nil
		},
		Logs: func() []logging.Entry {
			return []logging.Entry{{Line: `level=INFO msg="goscriber starting"`}, {Line: `level=WARN msg="something odd"`}}
		},
		OnRecordShortcut: func(on bool) { tw.recording = append(tw.recording, on) },
		Do:               tw.q.do,
	}
	if edit != nil {
		edit(&opts)
	}
	tw.SettingsWindow = NewSettingsWindow(test.NewTempApp(t), opts)
	return tw
}

// show opens the window on p and runs the background reads it starts.
func (tw *testWindow) show(p Page) {
	tw.Show(p)
	tw.q.drain()
}

// bound is the widget bound to a settings field.
func (tw *testWindow) bound(t *testing.T, key string) fyne.CanvasObject {
	t.Helper()
	for _, b := range tw.bindings {
		if b.key == key {
			return b.widget
		}
	}
	t.Fatalf("no widget is bound to %s", key)
	return nil
}

// pageObjects are the objects drawn on page p, widgets' insides included.
func (tw *testWindow) pageObjects(p Page) []fyne.CanvasObject {
	content := tw.items[p].Content
	content.Resize(fyne.NewSize(700, 2000))
	return test.LaidOutObjects(content)
}

// pageText joins the text of every label and button on page p.
func (tw *testWindow) pageText(p Page) string {
	var out []string
	for _, o := range tw.pageObjects(p) {
		switch o := o.(type) {
		case *widget.Label:
			if o.Visible() {
				out = append(out, o.Text)
			}
		case *widget.Hyperlink:
			out = append(out, o.Text)
		case *widget.Button:
			if o.Visible() {
				out = append(out, "["+o.Text+"]")
			}
		}
	}
	return strings.Join(out, "\n")
}

// buttons are the visible buttons on page p, by text.
func (tw *testWindow) buttons(p Page) map[string]*widget.Button {
	out := map[string]*widget.Button{}
	for _, o := range tw.pageObjects(p) {
		if b, ok := o.(*widget.Button); ok && b.Visible() {
			out[b.Text] = b
		}
	}
	return out
}

// reopen reads the settings file again, as the next launch would.
func (tw *testWindow) reopen(t *testing.T) config.Settings {
	t.Helper()
	s, _, err := config.Load(tw.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// fakeFiles is the model files of the built-in catalog, in memory.
type fakeFiles struct {
	cat *models.Catalog

	mu      sync.Mutex
	ready   map[string]bool // by "id/QUANT"
	partial map[string]int64
	fail    error
	gate    chan struct{} // when set, Download waits for it after its first progress
	deleted []string
}

func newFakeFiles() *fakeFiles {
	return &fakeFiles{cat: models.Default(), ready: map[string]bool{}, partial: map[string]int64{}}
}

func fileKey(id, quant string) string { return id + "/" + strings.ToUpper(quant) }

func (f *fakeFiles) Catalog() *models.Catalog { return f.cat }

func (f *fakeFiles) deletedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deleted)
}

func (f *fakeFiles) setGate(gate chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gate = gate
}

func (f *fakeFiles) setFail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
}

func (f *fakeFiles) setReady(id, quant string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ready[fileKey(id, quant)] = true
}

func (f *fakeFiles) List() []models.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.Status
	for i := range f.cat.Models {
		m := &f.cat.Models[i]
		for _, file := range m.Files {
			st := models.Status{Model: m, File: file, State: models.NotDownloaded, Done: f.partial[fileKey(m.ID, file.Quant)]}
			if f.ready[fileKey(m.ID, file.Quant)] {
				st.State, st.Path, st.Done = models.Ready, "/models/"+file.Name, file.Size
			}
			out = append(out, st)
		}
	}
	return out
}

func (f *fakeFiles) Download(ctx context.Context, id, quant string, progress chan<- models.Progress) error {
	m, _ := f.cat.Model(id)
	file, _ := m.File(quant)
	progress <- models.Progress{Model: id, Quant: file.Quant, File: file.Name, Done: file.Size / 4, Total: file.Size}
	f.mu.Lock()
	gate, fail := f.gate, f.fail
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail != nil {
		return fail
	}
	f.setReady(id, file.Quant)
	return nil
}

func (f *fakeFiles) Delete(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	for k := range f.ready {
		if strings.HasPrefix(k, id+"/") {
			delete(f.ready, k)
		}
	}
	return nil
}

func TestSettingsWindowIsCreatedOnFirstShow(t *testing.T) {
	tw := newTestWindow(t, nil)
	windows := len(tw.app.Driver().AllWindows()) // the test driver starts with one
	if tw.Window() != nil || len(tw.app.Driver().AllWindows()) != windows {
		t.Fatal("no window should exist before Show")
	}
	if tw.Page() != PageGeneral {
		t.Errorf("page before Show = %v", tw.Page())
	}

	tw.show(PageModels)
	w := tw.Window()
	if w == nil || w.Title() != SettingsTitle {
		t.Fatalf("window = %v", w)
	}
	if tw.Page() != PageModels {
		t.Errorf("page = %v, want Models", tw.Page())
	}
	if w.Content() != tw.tabs {
		t.Error("the pages should fill the window")
	}

	tw.show(PageAbout)
	if tw.Window() != w || len(tw.app.Driver().AllWindows()) != windows+1 {
		t.Error("Show should reuse the window")
	}
	if tw.Page() != PageAbout {
		t.Errorf("page = %v, want About", tw.Page())
	}
}

func TestSettingsWindowPages(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.show(PageGeneral)
	want := []Page{PageGeneral, PageModels, PageAdvanced, PagePostProcessing, PagePermissions, PageHistory, PageAbout}
	if got := tw.Pages(); !slices.Equal(got, want) {
		t.Errorf("pages %v, want %v: Debug only in debug mode", got, want)
	}
	for i, it := range tw.tabs.Items {
		if it.Text != want[i].String() {
			t.Errorf("sidebar item %d is %q, want %q", i, it.Text, want[i])
		}
	}
	tw.show(PageDebug)
	if tw.Page() != PageGeneral {
		t.Errorf("Show(Debug) outside debug mode opened %v", tw.Page())
	}

	about := tw.pageText(PageAbout)
	for _, s := range []string{
		"v1.2.3", "0.3.0", info.SettingsFile, info.LogsDir, "Handy", "MIT, © CJ Pais",
		"NeMo-Speech.cpp", "Apache-2.0, © NVIDIA", "Fyne", "Nemotron Streaming 3.5", "· OpenMDW-1.1", "· CC-BY-4.0", "Silero VAD",
	} {
		if !strings.Contains(about, s) {
			t.Errorf("About is missing %q:\n%s", s, about)
		}
	}
	if !strings.Contains(tw.pageText(PageHistory), HistoryUnavailable) {
		t.Error("History without a store should say dictations are not being saved")
	}

	noEngine := newTestWindow(t, func(o *SettingsOptions) { o.Info.EngineVersion = "" })
	noEngine.show(PageAbout)
	if !strings.Contains(noEngine.pageText(PageAbout), EngineNotBuiltIn) {
		t.Error("About should say the engine is not built in")
	}
}

// settingsKeys are the JSON names of the Settings fields, by field index.
func settingsKeys() map[string][]int {
	keys := map[string][]int{}
	typ := reflect.TypeFor[config.Settings]()
	for i := range typ.NumField() {
		f := typ.Field(i)
		keys[strings.Split(f.Tag.Get("json"), ",")[0]] = f.Index
	}
	return keys
}

// Every settings field can be changed from the window: each has a widget
// bound to it, the binding points at the field its key names, and the
// widget is on a page. Linux shows every field: the paste keys and typing
// tool are Linux's.
func TestEveryFieldIsBound(t *testing.T) {
	tw := newTestWindow(t, func(o *SettingsOptions) { o.ForceDebug = true; o.Info.OS = "linux" })
	tw.show(PageGeneral)

	keys := settingsKeys()
	bound := map[string]bool{}
	var drawn []fyne.CanvasObject
	for _, p := range tw.Pages() {
		drawn = append(drawn, tw.pageObjects(p)...)
	}
	for _, b := range tw.bindings {
		bound[b.key] = true
		index, ok := keys[b.key]
		if !ok {
			t.Errorf("%q is bound but is no settings field", b.key)
			continue
		}
		var s config.Settings
		want := reflect.ValueOf(&s).Elem().FieldByIndex(index).Addr().Pointer()
		if got := reflect.ValueOf(b.field(&s)).Pointer(); got != want {
			t.Errorf("the widget bound to %q changes another field", b.key)
		}
		if !slices.Contains(drawn, b.widget) {
			t.Errorf("the widget bound to %q is on no page", b.key)
		}
	}
	for key := range keys {
		if key != "schema_version" && !bound[key] {
			t.Errorf("%s has no widget in the settings window: bind it on a page (see settings_bind.go)", key)
		}
	}
}

// What the user changes is saved at once and is there at the next launch.
func TestSettingsPersistAcrossRestarts(t *testing.T) {
	tw := newTestWindow(t, func(o *SettingsOptions) { o.ForceDebug = true })
	tw.show(PageGeneral)

	tw.bound(t, "paste_method").(*widget.Select).SetSelected("Type the text")
	test.Tap(tw.bound(t, "auto_submit").(*widget.Check))
	tw.bound(t, "streaming_latency").(*widget.Select).SetSelected("Most accurate")
	tw.bound(t, "model_unload_timeout").(*widget.Select).SetSelected("Never")
	delay := tw.bound(t, "paste_delay_ms").(*settingSlider)
	delay.slider.SetValue(250)
	delay.save()
	tw.bound(t, "history_limit").(*widget.Entry).SetText("42")
	tw.bound(t, "custom_words").(*WordList).Type("Kowalczyk, NeMo")
	tw.bound(t, "log_level").(*widget.Select).SetSelected("Debug")
	tw.bound(t, "microphone").(*widget.Select).SetSelected("USB Mic")
	in := tw.bound(t, "transcribe_shortcut").(*ShortcutInput)
	test.Tap(in)
	in.KeyDown(&fyne.KeyEvent{Name: "LeftControl"})
	in.KeyDown(&fyne.KeyEvent{Name: "LeftShift"})
	in.KeyDown(&fyne.KeyEvent{Name: fyne.KeyK})

	s := tw.reopen(t)
	want := tw.store.Get()
	if s.PasteMethod != config.PasteDirect || !s.AutoSubmit || s.StreamingLatency != config.LatencyAccurate ||
		s.ModelUnloadTimeout != config.UnloadNever || s.PasteDelayMS != 250 || s.HistoryLimit != 42 ||
		!slices.Equal(s.CustomWords, []string{"Kowalczyk", "NeMo"}) || s.LogLevel != config.LogDebug ||
		s.Microphone != "USB Mic" || s.TranscribeShortcut != "ctrl+shift+k" {
		t.Errorf("after a restart: %+v", s)
	}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("the file has %+v, the store %+v", s, want)
	}
}

// A change made elsewhere, such as in the settings file or from the tray,
// shows in the window.
func TestSettingsWindowFollowsOutsideChanges(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.show(PageAdvanced)
	if err := tw.store.Update(func(s *config.Settings) {
		s.PasteMethod, s.VADEnabled, s.SilenceThresholdDB = config.PasteNone, false, -45
		s.ModelUnloadTimeout = 45
	}); err != nil {
		t.Fatal(err)
	}
	tw.SetSettings(tw.store.Get())
	if got := tw.bound(t, "paste_method").(*widget.Select).Selected; got != "Copy only, never paste" {
		t.Errorf("paste method shows %q", got)
	}
	if tw.bound(t, "vad_enabled").(*widget.Check).Checked {
		t.Error("VAD still checked")
	}
	if ss := tw.bound(t, "silence_threshold_db").(*settingSlider); ss.slider.Value != -45 || ss.value.Text != "-45 dBFS" {
		t.Errorf("silence slider %v %q", ss.slider.Value, ss.value.Text)
	}
	// A value the picker does not offer is shown as it is.
	if got := tw.bound(t, "model_unload_timeout").(*widget.Select).Selected; got != "After 45 seconds" {
		t.Errorf("unload timeout shows %q", got)
	}
	if got := tw.store.Get(); got.PasteMethod != config.PasteNone {
		t.Errorf("showing the change saved something else: %+v", got)
	}
}

// The VAD's offset can never pass its onset: moving one moves the other.
func TestVADThresholdsStayInOrder(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.show(PageAdvanced)
	onset := tw.bound(t, "vad_onset").(*settingSlider)
	onset.slider.SetValue(0.2)
	onset.save()
	if s := tw.store.Get(); s.VADOnset != 0.2 || s.VADOffset != 0.2 {
		t.Errorf("onset %v offset %v, want both 0.2", s.VADOnset, s.VADOffset)
	}
	offset := tw.bound(t, "vad_offset").(*settingSlider)
	offset.slider.SetValue(0.9)
	offset.save()
	if s := tw.store.Get(); s.VADOffset != 0.2 || offset.slider.Value != 0.2 {
		t.Errorf("offset %v (slider %v), want it held at the onset", s.VADOffset, offset.slider.Value)
	}
}

// A slider saves when the user lets go of it, not at every step of a drag.
func TestSliderSavesWhenLetGo(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.show(PageGeneral)
	hold := tw.bound(t, "hold_threshold_ms").(*settingSlider)
	for _, v := range []float64{400, 500, 600} { // what a drag does at each step
		hold.slider.Value = v
		hold.slider.OnChanged(v)
	}
	if got := tw.store.Get().HoldThresholdMS; got != 300 {
		t.Errorf("saved %d while dragging", got)
	}
	if hold.value.Text != "600 ms" {
		t.Errorf("label %q", hold.value.Text)
	}
	hold.slider.OnChangeEnded(600)
	if got := tw.store.Get().HoldThresholdMS; got != 600 {
		t.Errorf("saved %d once let go, want 600", got)
	}
	hold.slider.TypedKey(&fyne.KeyEvent{Name: fyne.KeyRight}) // the keyboard saves each step
	if got := tw.store.Get().HoldThresholdMS; got != 650 {
		t.Errorf("saved %d after the right arrow, want 650", got)
	}
}

// typeShortcut types sc on a window's canvas. Fyne's test canvas wraps the
// software canvas, which handles shortcuts, without passing them on.
func typeShortcut(t *testing.T, c fyne.Canvas, sc fyne.Shortcut) {
	t.Helper()
	if s, ok := c.(fyne.Shortcutable); ok {
		s.TypedShortcut(sc)
		return
	}
	inner, ok := reflect.ValueOf(c).Elem().Field(0).Interface().(fyne.Shortcutable)
	if !ok {
		t.Fatal("the canvas takes no shortcuts")
	}
	inner.TypedShortcut(sc)
}

func TestDebugShortcutTogglesTheDebugPage(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.show(PageAdvanced)
	typeShortcut(t, tw.Window().Canvas(), DebugShortcut)
	if !tw.store.Get().DebugMode || !slices.Contains(tw.Pages(), PageDebug) || tw.Page() != PageDebug {
		t.Fatalf("debug mode %v, pages %v, on %v", tw.store.Get().DebugMode, tw.Pages(), tw.Page())
	}
	text := tw.pageText(PageDebug)
	if !strings.Contains(text, `msg="something odd"`) {
		t.Errorf("the log viewer shows %q", text)
	}
	typeShortcut(t, tw.Window().Canvas(), DebugShortcut)
	if tw.store.Get().DebugMode || slices.Contains(tw.Pages(), PageDebug) || tw.Page() != PageGeneral {
		t.Errorf("debug mode %v, pages %v, on %v", tw.store.Get().DebugMode, tw.Pages(), tw.Page())
	}

	// --debug keeps the page whatever the setting says.
	forced := newTestWindow(t, func(o *SettingsOptions) { o.ForceDebug = true })
	forced.show(PageDebug)
	if forced.Page() != PageDebug {
		t.Errorf("--debug: on %v", forced.Page())
	}
}

func TestSettingsWindowCloseHides(t *testing.T) {
	closed := 0
	tw := newTestWindow(t, func(o *SettingsOptions) { o.OnClose = func() { closed++ } })
	tw.show(PageGeneral)
	w := tw.Window()
	tw.closeRequested()
	if closed != 1 || tw.Visible() {
		t.Errorf("onClose ran %d times, visible %v", closed, tw.Visible())
	}
	tw.show(PageModels)
	if tw.Window() != w {
		t.Error("closing should keep the window for the next Show")
	}

	// A nil OnClose only hides.
	tw = newTestWindow(t, nil)
	tw.show(PageGeneral)
	tw.closeRequested()
}

func TestSettingsWindowBanner(t *testing.T) {
	tw := newTestWindow(t, nil)
	acted, dismissed := 0, 0
	b := &Banner{
		Error: true, Title: "No model downloaded", Message: "Download a speech model to start dictating.",
		ActionLabel: "Download a model", OnAction: func() { acted++ }, OnDismiss: func() { dismissed++ },
	}
	tw.SetBanner(b) // before the window exists: shown when it opens
	if tw.Visible() {
		t.Error("visible before Show")
	}
	tw.show(PageGeneral)
	if !tw.Visible() || tw.Banner() != b {
		t.Fatal("the window should be visible with the banner")
	}
	content, ok := tw.Window().Content().(*fyne.Container)
	if !ok || content == nil {
		t.Fatal("the banner should sit above the pages")
	}

	var texts []string
	buttons := map[string]*widget.Button{}
	for _, o := range test.LaidOutObjects(content.Objects[1]) { // the banner; Objects[0] is the pages
		switch o := o.(type) {
		case *widget.Label:
			texts = append(texts, o.Text)
		case *widget.Button:
			buttons[o.Text] = o
		}
	}
	if !slices.Contains(texts, "No model downloaded") || !slices.Contains(texts, "Download a speech model to start dictating.") {
		t.Errorf("banner text %q", texts)
	}
	test.Tap(buttons["Download a model"])
	test.Tap(buttons["Dismiss"])
	if acted != 1 || dismissed != 1 {
		t.Errorf("acted %d, dismissed %d", acted, dismissed)
	}

	tw.SetBanner(nil)
	if tw.Window().Content() != tw.tabs {
		t.Error("without a banner the pages should fill the window again")
	}
}

func TestSettingsPermissionsPage(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.show(PagePermissions)
	if got := tw.pageText(PagePermissions); !strings.Contains(got, PermissionsNone) {
		t.Errorf("page %q, want the no-permissions note", got)
	}
	opened := 0
	tw.SetPermissions([]PermissionRow{
		{Title: "Microphone", Why: "To hear you.", Status: "denied", OnOpen: func() { opened++ }},
		{Title: "Accessibility", Why: "To paste.", Status: "granted", OK: true},
	})
	got := tw.pageText(PagePermissions)
	for _, want := range []string{"✗ Microphone: denied", "To hear you.", "✓ Accessibility: granted"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from %q", want, got)
		}
	}
	buttons := tw.buttons(PagePermissions)
	if len(buttons) != 1 {
		t.Fatalf("buttons %v, want one for the denied permission", buttons)
	}
	test.Tap(buttons["Open System Settings"])
	if opened != 1 {
		t.Error("the button should open System Settings")
	}
}

// Filler words keep apart the built-in list (null) and no words at all
// (an empty list).
func TestFillerWordsKeepBuiltInAndNoneApart(t *testing.T) {
	tw := newTestWindow(t, func(o *SettingsOptions) { o.DefaultFillerWords = []string{"um", "uh"} })
	tw.show(PageAdvanced)
	words := tw.bound(t, "custom_filler_words").(*WordList)
	if !slices.Equal(words.Words(), []string{"um", "uh"}) {
		t.Fatalf("the built-in list shows %v", words.Words())
	}
	words.Remove("uh")
	if got := tw.store.Get().CustomFillerWords; !slices.Equal(got, []string{"um"}) {
		t.Errorf("own list %v", got)
	}
	words.Remove("um")
	if got := tw.store.Get().CustomFillerWords; got == nil || len(got) != 0 {
		t.Errorf("removing every word saved %#v, want an empty list", got)
	}
	if got := tw.reopen(t).CustomFillerWords; got == nil || len(got) != 0 {
		t.Errorf("after a restart %#v, want an empty list", got)
	}
	test.Tap(tw.buttons(PageAdvanced)["Use the built-in list"])
	if got := tw.store.Get().CustomFillerWords; got != nil {
		t.Errorf("the built-in list saved %#v, want null", got)
	}
	if !slices.Equal(words.Words(), []string{"um", "uh"}) {
		t.Errorf("shows %v", words.Words())
	}
}

func TestSettingsLanguagePicker(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.files.setReady("nemotron-3.5-asr-streaming-0.6b", "Q8_0")
	if err := tw.store.Update(func(s *config.Settings) { s.SelectedModel = "nemotron-3.5-asr-streaming-0.6b" }); err != nil {
		t.Fatal(err)
	}
	tw.SetSettings(tw.store.Get())
	tw.show(PageModels)
	sel := tw.bound(t, "language").(*widget.Select)
	if !tw.languageBox.Visible() || sel.Selected != "Automatic" || len(sel.Options) != 33 {
		t.Fatalf("picker visible %v, selected %q, %d options", tw.languageBox.Visible(), sel.Selected, len(sel.Options))
	}
	tw.PickLanguage("es-ES")
	if got := tw.store.Get().Language; got != "es-ES" || tw.Language().Selected != "es-ES" {
		t.Errorf("language %q, picker %+v", got, tw.Language().Selected)
	}
	if tw.detectedLabel.Visible() {
		t.Error("no detected language yet")
	}
	tw.SetDetected("Spanish (US)")
	if !strings.Contains(tw.pageText(PageModels), "Detected in the last dictation: Spanish (US)") {
		t.Errorf("page text %q", tw.pageText(PageModels))
	}

	// Parakeet TDT v2 is English only: shown, locked, and the detected
	// language of another model is gone.
	if err := tw.store.Update(func(s *config.Settings) { s.SelectedModel = "parakeet-tdt-0.6b-v2" }); err != nil {
		t.Fatal(err)
	}
	tw.SetSettings(tw.store.Get())
	if !sel.Disabled() || sel.Selected != "English" || !strings.Contains(tw.pageText(PageModels), "English only") {
		t.Errorf("locked picker: disabled %v, selected %q", sel.Disabled(), sel.Selected)
	}
	if tw.Detected() != "" {
		t.Errorf("detected %q still shown for another model", tw.Detected())
	}

	// No model, no picker.
	if err := tw.store.Update(func(s *config.Settings) { s.SelectedModel = "" }); err != nil {
		t.Fatal(err)
	}
	tw.SetSettings(tw.store.Get())
	if tw.languageBox.Visible() {
		t.Error("the picker shows without a model")
	}
}

// A setting the store refuses is not kept: the widget shows the stored
// value again.
func TestRefusedChangeShowsTheStoredValue(t *testing.T) {
	tw := newTestWindow(t, func(o *SettingsOptions) { o.Store = refusingStore{o.Store} })
	tw.show(PageAdvanced)
	sel := tw.bound(t, "paste_method").(*widget.Select)
	sel.SetSelected("Type the text")
	if sel.Selected != "Paste through the clipboard" {
		t.Errorf("shows %q after a refused change", sel.Selected)
	}
}

type refusingStore struct{ SettingsStore }

func (refusingStore) Update(func(*config.Settings)) error { return errors.New("read-only") }

// The webhook URL is saved as it is typed once it is a URL; text that is
// not one is shown as wrong and leaves the setting alone, and clearing the
// field turns the webhook off.
func TestWebhookURLSetting(t *testing.T) {
	tw := newTestWindow(t, nil)
	tw.show(PageAdvanced)
	if !strings.Contains(tw.pageText(PageAdvanced), WebhookHint) {
		t.Error("the Advanced page does not explain the webhook")
	}
	e := tw.bound(t, "webhook_url").(*widget.Entry)

	e.SetText("  https://hooks.example.com/goscriber?token=t  ")
	if got := tw.store.Get().WebhookURL; got != "https://hooks.example.com/goscriber?token=t" {
		t.Errorf("saved %q", got)
	}
	e.SetText("hooks.example.com")
	if e.Validate() == nil {
		t.Error("a URL without a scheme passes")
	}
	if got := tw.store.Get().WebhookURL; got != "https://hooks.example.com/goscriber?token=t" {
		t.Errorf("text that is no URL saved %q", got)
	}
	e.SetText("")
	if got := tw.store.Get().WebhookURL; got != "" || e.Validate() != nil {
		t.Errorf("clearing the field left %q", got)
	}
	if s := tw.reopen(t); s.WebhookURL != "" {
		t.Errorf("after a restart: %q", s.WebhookURL)
	}
}
