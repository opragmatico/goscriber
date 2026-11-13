package ui

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
)

func newTestOverlay(t *testing.T, style config.OverlayStyle) (*Overlay, *int) {
	t.Helper()
	cancels := 0
	o := NewOverlay(test.NewTempApp(t), OverlayOptions{
		Style:    style,
		Position: config.OverlayBottom,
		OnCancel: func() { cancels++ },
	})
	return o, &cancels
}

func TestOverlayRecordingThenTranscribing(t *testing.T) {
	o, cancels := newTestOverlay(t, config.OverlayMinimal)
	if o.Window() != nil || o.Visible() {
		t.Fatal("nothing should be on screen before a recording")
	}
	t0 := time.Unix(1000, 0)
	o.SetState(OverlayRecording, t0)
	if !o.Visible() || o.Window() == nil {
		t.Fatal("recording should show the overlay")
	}
	if !o.meter.Visible() || !o.clock.Visible() || o.status.Visible() || o.dot.FillColor != overlayRed {
		t.Error("recording shows the red dot, the meter and the clock")
	}
	if o.Elapsed() != "0:00" {
		t.Errorf("clock %q", o.Elapsed())
	}
	o.Tick(t0.Add(65*time.Second + 300*time.Millisecond))
	if o.Elapsed() != "1:05" {
		t.Errorf("clock %q after 65 s", o.Elapsed())
	}
	if s := o.Window().Canvas().Size(); s.Width != overlayWidth || s.Height != overlayHeight {
		t.Errorf("window %v, want the minimal pill", s)
	}

	o.SetLevel(audio.Level{RMS: 1})
	o.SetLevel(audio.Level{RMS: 0.0005})
	lv := o.Levels()
	if lv[len(lv)-2] != 1 || lv[len(lv)-1] != 0 || lv[0] != 0 {
		t.Errorf("meter %v, want full scale then silence at the right", lv)
	}

	o.SetState(OverlayTranscribing, t0.Add(70*time.Second))
	if !o.Visible() || o.meter.Visible() || o.clock.Visible() || !o.status.Visible() || o.status.Text != LabelTranscribingOverlay {
		t.Error("transcribing replaces the meter and clock with the label")
	}
	if o.dot.FillColor == overlayRed {
		t.Error("the dot should not stay red while transcribing")
	}

	test.Tap(o.cancel)
	if *cancels != 1 {
		t.Errorf("cancel ran %d times", *cancels)
	}

	o.SetState(OverlayHidden, t0)
	if o.Visible() {
		t.Error("idle hides the overlay")
	}

	// The next recording starts from zero.
	o.SetState(OverlayRecording, t0.Add(time.Hour))
	if o.Elapsed() != "0:00" || o.Levels()[meterBars-2] != 0 {
		t.Errorf("clock %q, meter %v: a new recording starts afresh", o.Elapsed(), o.Levels())
	}
}

func TestOverlayNoneShowsNothing(t *testing.T) {
	o, _ := newTestOverlay(t, config.OverlayNone)
	o.SetState(OverlayRecording, time.Now())
	if o.Visible() || o.Window() != nil {
		t.Error("the none style never shows the overlay")
	}

	// Switching to none while shown hides it; switching back shows it.
	o, _ = newTestOverlay(t, config.OverlayMinimal)
	o.SetState(OverlayRecording, time.Now())
	o.SetStyle(config.OverlayNone, config.OverlayTop)
	if o.Visible() {
		t.Error("the none style should hide the overlay at once")
	}
	o.SetStyle(config.OverlayMinimal, config.OverlayTop)
	if !o.Visible() {
		t.Error("leaving the none style mid-recording shows the overlay again")
	}
}

func TestOverlayLiveGrowsForInterimText(t *testing.T) {
	o, _ := newTestOverlay(t, config.OverlayLive)
	o.SetState(OverlayRecording, time.Now())
	if s := o.Window().Canvas().Size(); s.Width != overlayWidth {
		t.Errorf("window %v before any text", s)
	}
	o.SetInterim("hello there")
	if o.Interim() != "hello there" {
		t.Errorf("interim %q", o.Interim())
	}
	s := o.Window().Canvas().Size()
	if s.Width != overlayLiveWidth || s.Height <= overlayHeight {
		t.Errorf("window %v, want the live pill grown for the text", s)
	}

	// The minimal style never shows text.
	o.SetStyle(config.OverlayMinimal, config.OverlayBottom)
	if o.Interim() != "" || o.Window().Canvas().Size().Width != overlayWidth {
		t.Error("the minimal style shows no text")
	}
}

func TestOverlayHelpers(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0:00", 999 * time.Millisecond: "0:00", 59 * time.Second: "0:59",
		61 * time.Second: "1:01", 10*time.Minute + 5*time.Second: "10:05", -time.Second: "0:00",
	} {
		if got := formatElapsed(d); got != want {
			t.Errorf("formatElapsed(%v) = %q, want %q", d, got, want)
		}
	}
	if got := meterLevel(audio.Level{RMS: 0.0316}); got < 0.49 || got > 0.51 {
		t.Errorf("-30 dBFS fills %v of the meter, want half", got)
	}
	long := strings.Repeat("word ", 40) + "end"
	got := interimText(long)
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "end") || len([]rune(got)) != interimTail {
		t.Errorf("interim tail %q", got)
	}
	if interimText("short") != "short" {
		t.Error("short text is shown whole")
	}
}

func TestOverlayCancelHover(t *testing.T) {
	o, _ := newTestOverlay(t, config.OverlayMinimal)
	o.SetState(OverlayRecording, time.Now())
	r := test.WidgetRenderer(o.cancel).(*cancelRenderer)
	o.cancel.MouseIn(nil)
	if r.c.a.StrokeColor != overlayInk {
		t.Error("the cross should brighten under the pointer")
	}
	o.cancel.MouseOut()
	if r.c.a.StrokeColor != overlayDim {
		t.Error("the cross should dim again")
	}
	if got := o.cancel.MinSize(); got != fyne.NewSquareSize(overlayButton) {
		t.Errorf("button %v", got)
	}
}
