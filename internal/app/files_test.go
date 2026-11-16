package app

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
)

// writeTone writes d of a tone as a WAV file and returns its path.
func writeTone(t *testing.T, name string, d time.Duration) string {
	t.Helper()
	const rate = 16000
	samples := make([]float32, int(d*rate/time.Second))
	for i := range samples {
		samples[i] = float32(0.1 * math.Sin(2*math.Pi*220*float64(i)/rate))
	}
	path := filepath.Join(t.TempDir(), name)
	if err := audio.WriteWAVFile(path, samples, rate); err != nil {
		t.Fatal(err)
	}
	return path
}

// A file transcribed from the History page lands in the history under its
// name, with its audio as the recording, after progress up to its length.
func TestTranscribeFileLandsInHistory(t *testing.T) {
	home := isolate(t)
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, q := newTestShell(t, a, Options{})
	path := writeTone(t, "interview.wav", 35*time.Second)

	var mu sync.Mutex
	var last, total time.Duration
	e, err := sh.historyActions().TranscribeFile(context.Background(), path, func(done, all time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		last, total = done, all
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if last != 35*time.Second || total != 35*time.Second {
		t.Errorf("progress ended at %v of %v", last, total)
	}
	mu.Unlock()
	// 35 s without a pause is decoded in two chunks, each "hello from the
	// test" from the fake engine.
	if e.ID == 0 || e.Source != "interview.wav" || e.Text != "hello from the test hello from the test" || e.Model != "m" || e.Duration != 35*time.Second {
		t.Fatalf("entry %+v", e)
	}
	samples, rate, err := audio.ReadWAVFile(e.Recording)
	if err != nil || rate != 16000 || len(samples) != 35*16000 {
		t.Errorf("recording %d samples at %d Hz, %v", len(samples), rate, err)
	}
	if out := sh.svc.output.(*testOutput); len(out.delivered()) != 0 {
		t.Errorf("a file's transcript was pasted: %q", out.delivered())
	}

	// With the history keeping nothing, the transcript still comes back.
	if err := sh.rt.Settings.Update(func(s *config.Settings) { s.HistoryLimit = 0 }); err != nil {
		t.Fatal(err)
	}
	q.waitUI(t, "the new retention", func() bool { return sh.history.Retention().Limit == 0 })
	e, err = sh.transcribeFile(context.Background(), path, nil)
	if err != nil || e.ID != 0 || !strings.HasPrefix(e.Text, "hello from the test") {
		t.Errorf("with no history kept: %+v, %v", e, err)
	}
}

func TestTranscribeFileErrors(t *testing.T) {
	home := isolate(t)
	writeSettings(t, home, `{"schema_version": 1, "selected_model": "m"}`)
	a := &trayApp{plainApp: plainApp{App: test.NewTempApp(t)}}
	sh, _ := newTestShell(t, a, Options{})

	notAudio := filepath.Join(t.TempDir(), "notes.wav")
	if err := os.WriteFile(notAudio, []byte("not audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.transcribeFile(context.Background(), notAudio, nil); !errors.Is(err, audio.ErrUnsupportedFile) || strings.Contains(err.Error(), notAudio) {
		t.Errorf("a text file: %v, want ErrUnsupportedFile without the path, which the page names", err)
	}
	if _, err := sh.transcribeFile(context.Background(), filepath.Join(t.TempDir(), "gone.wav"), nil); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing file: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sh.transcribeFile(ctx, writeTone(t, "x.wav", time.Second), nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	if n, _ := sh.history.Count(context.Background()); n != 0 {
		t.Errorf("%d entries saved from failures", n)
	}
}
