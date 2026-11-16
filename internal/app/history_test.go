package app

import (
	"context"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/ui"
)

// testPlayer records what it plays.
type testPlayer struct {
	mu     sync.Mutex
	played []int // the number of samples of each sound
	device []string
}

func (p *testPlayer) Play(_ context.Context, s audio.Sound, device string, _ float64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.played = append(p.played, len(s.Samples))
	p.device = append(p.device, device)
	return nil
}

// Every dictation is saved with its recording; the History page's actions
// play it, transcribe it again, paste it again, and the history follows the
// retention settings at once.
func TestDictationHistory(t *testing.T) {
	home := isolate(t)
	testShortcuts.reset()
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m", "output_device": "Speakers"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	out := sh.svc.output.(*testOutput)
	if sh.history == nil {
		t.Fatal("the history did not open")
	}

	item(t, a.menu, ui.LabelStartDictation).Action()
	q.waitUI(t, "the recording icon", func() bool { return a.trayIcon == ui.TrayIcon(ui.TrayRecording) })
	item(t, a.menu, ui.LabelStopDictation).Action()
	q.waitUI(t, "the transcript", func() bool { return len(out.delivered()) == 1 })
	var count int
	for deadline := time.Now().Add(3 * time.Second); count == 0 && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		count, _ = sh.history.Count(context.Background())
	}
	entries, err := sh.history.List(context.Background(), 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("history %+v, %v", entries, err)
	}
	e := entries[0]
	if e.Text != "hello from the test" || e.Model != "m" || e.Duration <= 0 || e.Recording == "" {
		t.Errorf("entry %+v", e)
	}
	if _, err := os.Stat(e.Recording); err != nil {
		t.Errorf("no recording on disk: %v", err)
	}

	if err := sh.playRecording(context.Background(), e.Recording); err != nil {
		t.Fatal(err)
	}
	p := sh.svc.player.(*testPlayer)
	p.mu.Lock()
	if len(p.played) != 1 || p.played[0] == 0 || p.device[0] != "Speakers" {
		t.Errorf("played %v on %v", p.played, p.device)
	}
	p.mu.Unlock()

	again, err := sh.retranscribe(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != e.ID || again.Text != "hello from the test" || again.Recording != e.Recording {
		t.Errorf("after transcribing again %+v", again)
	}

	delay := pasteAgainDelay
	pasteAgainDelay = 0
	t.Cleanup(func() { pasteAgainDelay = delay })
	sh.settings.Show(ui.PageHistory)
	sh.pasteAgain("again, please")
	q.waitUI(t, "the paste", func() bool { return slices.Contains(out.delivered(), "again, please") })
	if sh.settings.Visible() {
		t.Error("the settings window should step aside for the paste")
	}

	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.RecordingRetention = config.RetentionNever }); err != nil {
		t.Fatal(err)
	}
	q.waitUI(t, "the recording to go", func() bool {
		got, _ := sh.history.List(context.Background(), 0)
		return len(got) == 1 && got[0].Recording == ""
	})
	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.HistoryLimit = 0 }); err != nil {
		t.Fatal(err)
	}
	q.waitUI(t, "the history to empty", func() bool {
		n, _ := sh.history.Count(context.Background())
		return n == 0
	})
}
