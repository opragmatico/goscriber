package ui

import (
	"fmt"
	"image/color"
	"math"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/opragmatico/goscriber/internal/config"
)

// SettingsStore is where the settings window reads the settings and saves
// the user's changes. *config.Store implements it.
type SettingsStore interface {
	Get() config.Settings
	Update(fn func(*config.Settings)) error
}

// A binding ties a widget of the settings window to one settings field:
// the widget shows the field, and a change the user makes is saved to the
// store at once. Every field but schema_version has one, which a test
// checks against the Settings struct.
type binding struct {
	// key is the field's JSON name, such as "paste_method".
	key string
	// field returns a pointer to the field in s, so the test can check that
	// key names it.
	field func(s *config.Settings) any
	// widget shows the field. Tests find and drive it here.
	widget fyne.CanvasObject
	// load shows the field's value in the widget.
	load func(s config.Settings)
}

// bind registers a binding. The constructors below call it; a widget with
// its own logic, such as a model card, calls it directly.
func (w *SettingsWindow) bind(key string, field func(*config.Settings) any, obj fyne.CanvasObject, load func(config.Settings)) {
	b := &binding{key: key, field: field, widget: obj, load: load}
	w.bindings = append(w.bindings, b)
	if w.created {
		w.loadBinding(b)
	}
}

// show brings every widget in line with s, without saving anything.
func (w *SettingsWindow) show(s config.Settings) {
	w.cur = s
	if !w.created {
		return
	}
	for _, b := range w.bindings {
		w.loadBinding(b)
	}
	w.syncDebugPage()
}

func (w *SettingsWindow) loadBinding(b *binding) {
	w.loading = true
	defer func() { w.loading = false }()
	b.load(w.cur)
}

// change saves a change the user made. While the window is only showing
// settings (loading), widgets report the values it sets, and nothing is
// saved. A change the store refuses is logged and the widgets show the
// stored settings again.
func (w *SettingsWindow) change(fn func(*config.Settings)) {
	if w.loading {
		return
	}
	err := w.opts.Store.Update(fn)
	if err != nil {
		w.log().Warn("the setting could not be saved", "err", err)
	}
	w.show(w.opts.Store.Get())
}

// setField saves v into the field unless it holds v already.
func setField[T comparable](w *SettingsWindow, field func(*config.Settings) *T, v T) {
	if w.loading || *field(&w.cur) == v {
		return
	}
	w.change(func(s *config.Settings) { *field(s) = v })
}

// anyField adapts a typed field accessor to binding.field.
func anyField[T any](field func(*config.Settings) *T) func(*config.Settings) any {
	return func(s *config.Settings) any { return field(s) }
}

// boolSetting is a check box bound to a boolean field.
func boolSetting(w *SettingsWindow, key string, field func(*config.Settings) *bool) *widget.Check {
	c := widget.NewCheck("", nil)
	c.OnChanged = func(on bool) { setField(w, field, on) }
	w.bind(key, anyField(field), c, func(s config.Settings) { c.SetChecked(*field(&s)) })
	return c
}

// choice is one option of a choiceSetting: a value and what it is called.
type choice[T comparable] struct {
	value T
	label string
}

// choiceSetting is a drop-down bound to a field that takes one of choices.
// A stored value that is not among them is shown with other's label, so
// the field never looks unset; nil other uses fmt.Sprint.
func choiceSetting[T comparable](w *SettingsWindow, key string, field func(*config.Settings) *T, choices []choice[T], other func(T) string) *widget.Select {
	if other == nil {
		other = func(v T) string { return fmt.Sprint(v) }
	}
	shown := choices
	sel := widget.NewSelect(nil, nil)
	sel.OnChanged = func(label string) {
		for _, c := range shown {
			if c.label == label {
				setField(w, field, c.value)
				return
			}
		}
	}
	w.bind(key, anyField(field), sel, func(s config.Settings) {
		v := *field(&s)
		shown = choices
		found := false
		for _, c := range choices {
			found = found || c.value == v
		}
		if !found {
			shown = append(append([]choice[T](nil), choices...), choice[T]{v, other(v)})
		}
		labels := make([]string, len(shown))
		selected := ""
		for i, c := range shown {
			labels[i] = c.label
			if c.value == v {
				selected = c.label
			}
		}
		sel.SetOptions(labels)
		sel.SetSelected(selected)
	})
	return sel
}

// sliderValueWidth is as wide as any slider's value gets.
const sliderValueWidth = "-100 dBFS"

// number is a numeric settings field.
type number interface{ ~int | ~float64 }

// settingSlider is a slider bound to a numeric field, with the value
// written next to it.
type settingSlider struct {
	widget.BaseWidget
	content fyne.CanvasObject
	slider  *widget.Slider
	value   *widget.Label
	// save saves the slider's value, as letting go of it does.
	save func()
	// fix, if set, adjusts other fields in the same change, so that the
	// settings stay valid (the VAD offset may not exceed its onset).
	fix func(s *config.Settings)
}

// sliderSetting is a slider from lo to hi in steps of step, bound to a
// numeric field and labelled with format. Its value is saved when the user
// lets go of it, or at once when it moves by keyboard, so a drag saves once
// rather than at every step. A stored value outside the range widens it.
func sliderSetting[T number](w *SettingsWindow, key string, field func(*config.Settings) *T, lo, hi, step float64, format func(T) string) *settingSlider {
	sl := widget.NewSlider(lo, hi)
	sl.Step = step
	label := widget.NewLabel("")
	label.Alignment = fyne.TextAlignTrailing
	label.TextStyle.Monospace = true
	// Every value is written in a column as wide as the widest, so sliders
	// line up and do not move as their value changes.
	column := canvas.NewRectangle(color.Transparent)
	column.SetMinSize(fyne.NewSize(widget.NewLabelWithStyle(sliderValueWidth, fyne.TextAlignTrailing, label.TextStyle).MinSize().Width, 0))
	ss := &settingSlider{slider: sl, value: label, content: container.NewBorder(nil, nil, nil, container.NewStack(column, label), sl)}
	ss.ExtendBaseWidget(ss)
	isInt := T(1)/T(2) == 0
	// Values are rounded to the step, and to as many decimals as it has,
	// so 7 steps of 0.05 are saved as 0.35, not 0.35000000000000003.
	scale := math.Pow(10, max(0, math.Ceil(-math.Log10(step))))
	convert := func(v float64) T {
		if isInt {
			return T(math.Round(v))
		}
		return T(math.Round(math.Round(v/step)*step*scale) / scale)
	}
	ss.save = func() {
		v := convert(sl.Value)
		if w.loading || *field(&w.cur) == v {
			return
		}
		w.change(func(s *config.Settings) {
			*field(s) = v
			if ss.fix != nil {
				ss.fix(s)
			}
		})
	}
	sl.OnChanged = func(v float64) { label.SetText(format(convert(v))) }
	sl.OnChangeEnded = func(float64) { ss.save() }
	w.bind(key, anyField(field), ss, func(s config.Settings) {
		v := float64(*field(&s))
		sl.Min, sl.Max = min(lo, v), max(hi, v)
		sl.SetValue(v)
		label.SetText(format(*field(&s)))
	})
	return ss
}

// CreateRenderer implements fyne.Widget.
func (ss *settingSlider) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(ss.content)
}

// intEntrySetting is a text field for a whole number from lo to hi. A
// valid number is saved as it is typed.
func intEntrySetting(w *SettingsWindow, key string, field func(*config.Settings) *int, lo, hi int) *widget.Entry {
	e := widget.NewEntry()
	parse := func(text string) (int, error) {
		n, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || n < lo || n > hi {
			return 0, fmt.Errorf("enter a whole number from %d to %d", lo, hi)
		}
		return n, nil
	}
	e.Validator = func(text string) error {
		_, err := parse(text)
		return err
	}
	e.OnChanged = func(text string) {
		if n, err := parse(text); err == nil {
			setField(w, field, n)
		}
	}
	w.bind(key, anyField(field), e, func(s config.Settings) {
		// Leave what the user is typing alone when it already says the same.
		if n, err := parse(e.Text); err != nil || n != *field(&s) {
			e.SetText(strconv.Itoa(*field(&s)))
		}
	})
	return e
}

// Formats for slider values.
func formatMS(v int) string           { return fmt.Sprintf("%d ms", v) }
func formatPercent(v float64) string  { return fmt.Sprintf("%.0f %%", v*100) }
func formatFraction(v float64) string { return fmt.Sprintf("%.2f", v) }

// settingsPage lays out a page: its title over groups of settings, in a
// scroll so a long page fits a small window.
func settingsPage(title string, body ...fyne.CanvasObject) fyne.CanvasObject {
	return container.NewVScroll(page(title, body...))
}

// group is a titled group of settings rows.
func group(title string, rows ...fyne.CanvasObject) fyne.CanvasObject {
	return section(title, rows...)
}

// section is a titled part of a page.
func section(title string, body ...fyne.CanvasObject) fyne.CanvasObject {
	heading := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	return container.NewVBox(append([]fyne.CanvasObject{widget.NewSeparator(), heading}, body...)...)
}

// labelColumn lines up the names of a page's settings: every row of the
// page shares one, so the widgets start at the same place in every group.
type labelColumn struct{ labels []*widget.Label }

func (c *labelColumn) width() float32 {
	var w float32
	for _, l := range c.labels {
		w = max(w, l.MinSize().Width)
	}
	return w
}

// rowLayout puts a setting's name in its page's label column and the
// widget, with its hint below, in the rest of the row.
type rowLayout struct{ col *labelColumn }

func (r rowLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	label, body := objs[0].MinSize(), objs[1].MinSize()
	return fyne.NewSize(r.col.width()+theme.Padding()+body.Width, max(label.Height, body.Height))
}

func (r rowLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	w := r.col.width()
	objs[0].Move(fyne.NewPos(0, 0))
	objs[0].Resize(fyne.NewSize(w, objs[0].MinSize().Height))
	x := w + theme.Padding()
	objs[1].Move(fyne.NewPos(x, 0))
	objs[1].Resize(fyne.NewSize(size.Width-x, size.Height))
}

// row is one setting: its name in the page's label column, the widget and
// a line explaining it, which wraps to the width of the window.
func (w *SettingsWindow) row(label, hint string, obj fyne.CanvasObject) fyne.CanvasObject {
	name := widget.NewLabelWithStyle(label, fyne.TextAlignTrailing, fyne.TextStyle{Bold: true})
	if w.col == nil {
		w.col = &labelColumn{}
	}
	w.col.labels = append(w.col.labels, name)
	body := container.NewVBox(obj)
	if hint != "" {
		body.Add(note(hint))
	}
	return container.New(rowLayout{w.col}, name, body)
}

func page(title string, body ...fyne.CanvasObject) fyne.CanvasObject {
	heading := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	heading.SizeName = theme.SizeNameSubHeadingText
	return container.NewVBox(append([]fyne.CanvasObject{heading}, body...)...)
}

// note is a line of small print that wraps, such as a setting's hint.
func note(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.SizeName = theme.SizeNameCaptionText
	l.Wrapping = fyne.TextWrapWord
	return l
}

func paragraph(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	return l
}

func path(p string) *widget.Label {
	l := widget.NewLabelWithStyle(p, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	l.Wrapping = fyne.TextWrapBreak
	l.Selectable = true
	return l
}
