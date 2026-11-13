package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// levelMeter draws the microphone level as a row of bars, newest on the
// right, growing from the middle.
type levelMeter struct {
	widget.BaseWidget
	levels []float32
	bars   []*canvas.Rectangle
}

func newLevelMeter() *levelMeter {
	m := &levelMeter{levels: make([]float32, meterBars)}
	for range meterBars {
		r := canvas.NewRectangle(overlayInk)
		r.CornerRadius = meterBarWidth / 2
		m.bars = append(m.bars, r)
	}
	m.ExtendBaseWidget(m)
	return m
}

// setLevels shows levels, from 0 to 1, oldest first.
func (m *levelMeter) setLevels(levels []float32) {
	copy(m.levels, levels)
	m.Refresh()
}

func (m *levelMeter) MinSize() fyne.Size {
	return fyne.NewSize(meterBars*meterBarWidth+(meterBars-1)*meterBarGap, meterHeight)
}

func (m *levelMeter) CreateRenderer() fyne.WidgetRenderer {
	objs := make([]fyne.CanvasObject, len(m.bars))
	for i, b := range m.bars {
		objs[i] = b
	}
	return &meterRenderer{m: m, objs: objs}
}

type meterRenderer struct {
	m    *levelMeter
	objs []fyne.CanvasObject
}

func (r *meterRenderer) Layout(size fyne.Size) {
	for i, b := range r.m.bars {
		h := max(meterBarWidth, r.m.levels[i]*size.Height)
		b.Resize(fyne.NewSize(meterBarWidth, h))
		b.Move(fyne.NewPos(float32(i)*(meterBarWidth+meterBarGap), (size.Height-h)/2))
	}
}

func (r *meterRenderer) MinSize() fyne.Size { return r.m.MinSize() }

func (r *meterRenderer) Refresh() {
	r.Layout(r.m.Size())
	for _, b := range r.m.bars {
		b.Refresh()
	}
}

func (r *meterRenderer) Objects() []fyne.CanvasObject { return r.objs }
func (r *meterRenderer) Destroy()                     {}

// overlayCancel is the overlay's cancel button: a cross that brightens under
// the mouse pointer.
type overlayCancel struct {
	widget.BaseWidget
	onTap   func()
	hovered bool
	ring    *canvas.Circle
	a, b    *canvas.Line
}

var _ desktop.Hoverable = (*overlayCancel)(nil)

func newOverlayCancel(onTap func()) *overlayCancel {
	c := &overlayCancel{
		onTap: onTap,
		ring:  canvas.NewCircle(color.Transparent),
		a:     canvas.NewLine(overlayDim),
		b:     canvas.NewLine(overlayDim),
	}
	c.a.StrokeWidth, c.b.StrokeWidth = 1.6, 1.6
	c.ExtendBaseWidget(c)
	return c
}

// Tapped implements fyne.Tappable.
func (c *overlayCancel) Tapped(*fyne.PointEvent) {
	if c.onTap != nil {
		c.onTap()
	}
}

// MouseIn implements desktop.Hoverable.
func (c *overlayCancel) MouseIn(*desktop.MouseEvent) { c.hovered = true; c.Refresh() }

// MouseMoved implements desktop.Hoverable.
func (c *overlayCancel) MouseMoved(*desktop.MouseEvent) {}

// MouseOut implements desktop.Hoverable.
func (c *overlayCancel) MouseOut() { c.hovered = false; c.Refresh() }

func (c *overlayCancel) MinSize() fyne.Size { return fyne.NewSquareSize(overlayButton) }

func (c *overlayCancel) CreateRenderer() fyne.WidgetRenderer {
	return &cancelRenderer{c: c, objs: []fyne.CanvasObject{c.ring, c.a, c.b}}
}

type cancelRenderer struct {
	c    *overlayCancel
	objs []fyne.CanvasObject
}

func (r *cancelRenderer) Layout(size fyne.Size) {
	r.c.ring.Resize(size)
	r.c.ring.Move(fyne.NewPos(0, 0))
	inset := size.Width * 0.34
	r.c.a.Position1, r.c.a.Position2 = fyne.NewPos(inset, inset), fyne.NewPos(size.Width-inset, size.Height-inset)
	r.c.b.Position1, r.c.b.Position2 = fyne.NewPos(size.Width-inset, inset), fyne.NewPos(inset, size.Height-inset)
}

func (r *cancelRenderer) MinSize() fyne.Size { return r.c.MinSize() }

func (r *cancelRenderer) Refresh() {
	ink, ring := color.Color(overlayDim), color.Color(color.Transparent)
	if r.c.hovered {
		ink, ring = overlayInk, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x22}
	}
	r.c.a.StrokeColor, r.c.b.StrokeColor, r.c.ring.FillColor = ink, ink, ring
	r.Layout(r.c.Size())
	for _, o := range r.objs {
		o.Refresh()
	}
}

func (r *cancelRenderer) Objects() []fyne.CanvasObject { return r.objs }
func (r *cancelRenderer) Destroy()                     {}
