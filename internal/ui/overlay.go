package ui

import (
	"fmt"
	"image/color"
	"time"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/platform"
)

// OverlayState is what the recording overlay shows.
type OverlayState int

// Overlay states. The coordinator's steps after recording (transcribing,
// post-processing, output) all show as OverlayTranscribing.
const (
	OverlayHidden OverlayState = iota
	OverlayRecording
	OverlayTranscribing
)

func (s OverlayState) String() string {
	switch s {
	case OverlayHidden:
		return "hidden"
	case OverlayRecording:
		return "recording"
	case OverlayTranscribing:
		return "transcribing"
	default:
		return fmt.Sprintf("OverlayState(%d)", int(s))
	}
}

// Overlay geometry, in points.
const (
	overlayHeight    = 36
	overlayWidth     = 184 // minimal, and live with no text yet
	overlayLiveWidth = 380 // live, once there is text
	overlayTextLines = 2   // of interim text, in the live style
	overlayMargin    = 48  // from the top or bottom of the screen
	overlayPad       = 14
	overlayDot       = 10
	overlayButton    = 22
	meterBars        = 9
	meterBarWidth    = 3
	meterBarGap      = 2
	meterHeight      = 22
	// interimTail is how much of the interim text the live style shows: the
	// end of it, which is what is changing.
	interimTail = 110
)

// LabelTranscribingOverlay is what the overlay says while the transcript is
// on its way.
const LabelTranscribingOverlay = "Transcribing…"

var (
	overlayRed   = color.NRGBA{R: 0xff, G: 0x45, B: 0x3a, A: 0xff}
	overlayAmber = color.NRGBA{R: 0xff, G: 0xb3, B: 0x40, A: 0xff}
	overlayPill  = color.NRGBA{R: 0x16, G: 0x16, B: 0x18, A: 0xf2}
	overlayInk   = color.NRGBA{R: 0xf2, G: 0xf2, B: 0xf4, A: 0xff}
	overlayDim   = color.NRGBA{R: 0xf2, G: 0xf2, B: 0xf4, A: 0x99}
)

// OverlayOptions configure the overlay.
type OverlayOptions struct {
	Style    config.OverlayStyle
	Position config.OverlayPosition
	// OnCancel runs on the main thread when the cancel button is clicked.
	OnCancel func()
}

// Overlay is the small always-on-top pill that shows Goscriber is
// listening: a recording dot, a level meter, the elapsed time and a cancel
// button, or a transcribing indicator once recording stops. In the live
// style it grows to show the interim text.
//
// It must never take the keyboard from the app the user dictates into: the
// window is shown without focus (GLFW's focus-on-show is off while it is
// created), and on macOS it is made floating, visible on every Space and
// over full-screen apps, and click-through except over the cancel button.
//
// The window is created the first time there is something to show. Its
// methods must be called on the Fyne main thread.
type Overlay struct {
	app      fyne.App
	style    config.OverlayStyle
	position config.OverlayPosition
	onCancel func()

	state   OverlayState
	since   time.Time
	levels  []float32 // the meter, oldest first, each 0 to 1
	interim string

	win     fyne.Window
	native  any // the driver's native window context, once created
	shown   bool
	size    fyne.Size // what the content needs
	placed  fyne.Size // what the window was last given
	content *fyne.Container
	dot     *canvas.Circle
	meter   *levelMeter
	clock   *canvas.Text
	status  *canvas.Text
	cancel  *overlayCancel
	text    *widget.Label
}

// NewOverlay prepares the overlay. Nothing is on screen until SetState asks
// for it.
func NewOverlay(a fyne.App, opts OverlayOptions) *Overlay {
	o := &Overlay{app: a, onCancel: opts.OnCancel, levels: make([]float32, meterBars)}
	o.style, o.position = opts.Style, opts.Position
	return o
}

// State is what the overlay shows.
func (o *Overlay) State() OverlayState { return o.state }

// Visible reports whether the overlay is on screen.
func (o *Overlay) Visible() bool { return o.shown }

// Window is the overlay's window, or nil before it was first needed.
func (o *Overlay) Window() fyne.Window { return o.win }

// Elapsed is the time shown, as m:ss.
func (o *Overlay) Elapsed() string {
	if o.clock == nil {
		return ""
	}
	return o.clock.Text
}

// Interim is the interim text shown, or "" when none is.
func (o *Overlay) Interim() string {
	if o.text == nil || !o.text.Visible() {
		return ""
	}
	return o.text.Text
}

// Levels returns the meter's bars, oldest first, from 0 to 1.
func (o *Overlay) Levels() []float32 { return append([]float32(nil), o.levels...) }

// SetStyle changes the style and position. With the none style the overlay
// hides at once and stays hidden.
func (o *Overlay) SetStyle(style config.OverlayStyle, position config.OverlayPosition) {
	if style == o.style && position == o.position {
		return
	}
	o.style, o.position = style, position
	if o.state == OverlayHidden {
		return
	}
	if style == config.OverlayNone {
		o.hide()
		return
	}
	o.refresh(time.Now())
	o.show()
}

// SetState shows the overlay for recording or transcribing, or hides it.
// Recording starts the clock and clears the meter and the interim text.
func (o *Overlay) SetState(s OverlayState, now time.Time) {
	if s == o.state {
		return
	}
	prev := o.state
	o.state = s
	if s == OverlayHidden || o.style == config.OverlayNone {
		o.hide()
		return
	}
	if s == OverlayRecording || prev == OverlayHidden {
		o.since = now
		clear(o.levels)
		o.interim = ""
	}
	o.refresh(now)
	o.show()
}

// SetLevel adds a microphone level to the meter.
func (o *Overlay) SetLevel(l audio.Level) {
	copy(o.levels, o.levels[1:])
	o.levels[len(o.levels)-1] = meterLevel(l)
	if o.meter != nil && o.state == OverlayRecording {
		o.meter.setLevels(o.levels)
	}
}

// SetInterim shows the transcript so far, in the live style.
func (o *Overlay) SetInterim(text string) {
	if text == o.interim {
		return
	}
	o.interim = text
	if o.shown {
		o.refresh(time.Now())
		o.resize()
	}
}

// Tick updates the clock and, on macOS, lets clicks through everywhere but
// the cancel button, which follows the mouse pointer. Call it several times
// a second while the overlay is shown.
func (o *Overlay) Tick(now time.Time) {
	if !o.shown {
		return
	}
	if t := formatElapsed(now.Sub(o.since)); o.clock.Text != t {
		o.clock.Text = t
		o.clock.Refresh()
	}
	if o.native != nil {
		p := o.cancel.Position()
		s := o.cancel.Size()
		platform.OverlayClickThrough(o.native, p.X, p.Y, s.Width, s.Height)
	}
}

// refresh brings the widgets in line with the state.
func (o *Overlay) refresh(now time.Time) {
	if o.win == nil {
		o.create()
	}
	recording := o.state == OverlayRecording
	if recording {
		o.dot.FillColor = overlayRed
		o.meter.Show()
		o.clock.Show()
		o.status.Hide()
	} else {
		o.dot.FillColor = overlayAmber
		o.meter.Hide()
		o.clock.Hide()
		o.status.Show()
	}
	o.dot.Refresh()
	o.meter.setLevels(o.levels)
	o.clock.Text = formatElapsed(now.Sub(o.since))
	o.clock.Refresh()

	if text := interimText(o.interim); o.style == config.OverlayLive && text != "" {
		o.text.SetText(text)
		o.text.Show()
	} else {
		o.text.Hide()
	}
	o.size = o.wantSize()
	o.content.Refresh()
}

// wantSize is the window size for the style and the interim text.
func (o *Overlay) wantSize() fyne.Size {
	if o.text.Visible() {
		lines := float32(overlayTextLines) * (theme.TextSize() + 2*theme.LineSpacing() + 2)
		return fyne.NewSize(overlayLiveWidth, overlayHeight+lines+overlayPad/2)
	}
	return fyne.NewSize(overlayWidth, overlayHeight)
}

func (o *Overlay) create() {
	var win fyne.Window
	if d, ok := o.app.Driver().(desktop.Driver); ok {
		win = d.CreateSplashWindow()
	} else {
		win = o.app.NewWindow("Goscriber")
	}
	win.SetTitle("Goscriber")
	win.SetPadded(false)
	win.SetFixedSize(true)
	if dw, ok := win.(desktop.Window); ok {
		dw.RequestAlwaysOnTop()
	}

	o.dot = canvas.NewCircle(overlayRed)
	o.meter = newLevelMeter()
	o.clock = canvas.NewText("0:00", overlayInk)
	o.clock.TextStyle = fyne.TextStyle{Monospace: true}
	o.clock.TextSize = 12
	o.status = canvas.NewText(LabelTranscribingOverlay, overlayInk)
	o.status.TextSize = 12
	o.cancel = newOverlayCancel(o.cancelTapped)
	o.text = widget.NewLabel("")
	o.text.Wrapping = fyne.TextWrapWord
	o.text.Hide()

	// Square: on macOS the window itself is clipped to a pill, and
	// elsewhere a rounded rectangle would leave the window's light corners
	// showing.
	bg := canvas.NewRectangle(overlayPill)
	o.content = container.New(&pillLayout{o: o}, bg, o.dot, o.meter, o.clock, o.status, o.cancel, o.text)
	win.SetContent(container.NewThemeOverride(o.content, darkTheme{theme.DefaultTheme()}))
	o.win = win
}

// show puts the window on screen, at its size and place, without focus.
// Once shown it only follows size changes: showing it again would move it
// back to where Fyne last saw it.
func (o *Overlay) show() {
	if o.shown {
		o.resize()
		return
	}
	o.win.Resize(o.size)
	if nw, ok := o.win.(driver.NativeWindow); ok {
		withoutFocusOnShow(o.win.Show)
		if o.native == nil {
			nw.RunNative(func(ctx any) { o.native = ctx })
			platform.PrepareOverlay(o.native, overlayHeight/2)
		}
	} else {
		o.win.Show()
	}
	o.shown, o.placed = true, o.size
	o.place()
}

// resize fits the window to its content after it changed.
func (o *Overlay) resize() {
	if o.size == o.placed {
		return
	}
	o.placed = o.size
	o.win.Resize(o.size)
	o.place()
}

func (o *Overlay) place() {
	if o.native != nil {
		platform.PlaceOverlay(o.native, o.position == config.OverlayTop, overlayMargin)
	}
}

func (o *Overlay) hide() {
	if o.win != nil && o.shown {
		o.win.Hide()
	}
	o.shown = false
}

// cancelTapped runs when the cancel button is clicked. The click made
// Goscriber the active app, so the keyboard goes back to the app the user
// was in.
func (o *Overlay) cancelTapped() {
	if o.onCancel != nil {
		o.onCancel()
	}
	if o.native != nil {
		platform.YieldFocus()
	}
}

// pillLayout lays the overlay out: the dot, the meter and the clock (or the
// transcribing label) from the left, the cancel button on the right, and in
// the live style the interim text below.
type pillLayout struct{ o *Overlay }

func (l *pillLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	o := l.o
	objects[0].Resize(size) // the background
	objects[0].Move(fyne.NewPos(0, 0))
	mid := float32(overlayHeight) / 2

	x := float32(overlayPad)
	o.dot.Resize(fyne.NewSquareSize(overlayDot))
	o.dot.Move(fyne.NewPos(x, mid-overlayDot/2))
	x += overlayDot + 10

	mw := o.meter.MinSize()
	o.meter.Resize(mw)
	o.meter.Move(fyne.NewPos(x, mid-mw.Height/2))
	cs := o.clock.MinSize()
	o.clock.Resize(cs)
	o.clock.Move(fyne.NewPos(x+mw.Width+10, mid-cs.Height/2))
	ss := o.status.MinSize()
	o.status.Resize(ss)
	o.status.Move(fyne.NewPos(x, mid-ss.Height/2))

	o.cancel.Resize(fyne.NewSquareSize(overlayButton))
	o.cancel.Move(fyne.NewPos(size.Width-overlayPad/2-overlayButton, mid-overlayButton/2))

	o.text.Move(fyne.NewPos(overlayPad/2, overlayHeight-6))
	o.text.Resize(fyne.NewSize(size.Width-overlayPad, size.Height-overlayHeight))
}

func (l *pillLayout) MinSize([]fyne.CanvasObject) fyne.Size {
	if l.o.size.IsZero() {
		return fyne.NewSize(overlayWidth, overlayHeight)
	}
	return l.o.size
}

// meterLevel maps a level to a bar height from 0 to 1: -60 dBFS (about the
// silence threshold) and below is empty, full scale is full.
func meterLevel(l audio.Level) float32 {
	v := float32((audio.DBFS(l.RMS) + 60) / 60)
	return min(max(v, 0), 1)
}

// formatElapsed formats d as m:ss.
func formatElapsed(d time.Duration) string {
	s := max(int(d/time.Second), 0)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// interimText is the end of the interim text, as much as the live overlay
// shows.
func interimText(s string) string {
	if utf8.RuneCountInString(s) <= interimTail {
		return s
	}
	r := []rune(s)
	return "…" + string(r[len(r)-interimTail+1:])
}

// darkTheme is the default theme in its dark variant whatever the system
// uses, so the overlay's text reads on the dark pill.
type darkTheme struct{ fyne.Theme }

func (t darkTheme) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	return t.Theme.Color(n, theme.VariantDark)
}
