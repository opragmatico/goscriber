package ui

import (
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// WordList edits a list of words or short phrases: type one (or several,
// separated by commas) and add it, or remove one with its button. Words are
// kept in the order added, without duplicates that differ only in case.
type WordList struct {
	widget.BaseWidget

	content fyne.CanvasObject
	entry   *widget.Entry
	add     *widget.Button
	chips   *fyne.Container
	empty   *widget.Label
	words   []string

	// OnChanged receives the words after the user adds or removes one.
	OnChanged func(words []string)
}

// NewWordList returns an empty list; placeholder is the entry's hint, and
// empty what the list says when it has no words.
func NewWordList(placeholder, empty string) *WordList {
	l := &WordList{}
	l.entry = widget.NewEntry()
	l.entry.SetPlaceHolder(placeholder)
	l.entry.OnSubmitted = func(string) { l.addTyped() }
	l.add = widget.NewButtonWithIcon("Add", theme.ContentAddIcon(), l.addTyped)
	l.empty = note(empty)
	l.chips = container.New(layout.NewRowWrapLayout())
	l.content = container.NewVBox(container.NewBorder(nil, nil, nil, l.add, l.entry), l.empty, l.chips)
	l.ExtendBaseWidget(l)
	l.refresh()
	return l
}

// CreateRenderer implements fyne.Widget.
func (l *WordList) CreateRenderer() fyne.WidgetRenderer { return widget.NewSimpleRenderer(l.content) }

// Words are the words in the list.
func (l *WordList) Words() []string { return slices.Clone(l.words) }

// SetWords shows words. It reports nothing to OnChanged.
func (l *WordList) SetWords(words []string) {
	if slices.Equal(words, l.words) {
		return
	}
	l.words = slices.Clone(words)
	l.refresh()
}

// SetEmptyText changes what the list says when it has no words; "" says
// nothing.
func (l *WordList) SetEmptyText(text string) {
	l.empty.SetText(text)
	l.refresh()
}

// Type adds text as if the user typed it and pressed Add.
func (l *WordList) Type(text string) {
	l.entry.SetText(text)
	l.addTyped()
}

// Remove removes word as if the user pressed its button.
func (l *WordList) Remove(word string) {
	i := slices.Index(l.words, word)
	if i < 0 {
		return
	}
	l.words = slices.Delete(slices.Clone(l.words), i, i+1)
	l.refresh()
	l.changed()
}

func (l *WordList) addTyped() {
	added := false
	for part := range strings.SplitSeq(l.entry.Text, ",") {
		w := strings.Join(strings.Fields(part), " ")
		if w == "" || slices.ContainsFunc(l.words, func(have string) bool { return strings.EqualFold(have, w) }) {
			continue
		}
		l.words = append(slices.Clone(l.words), w)
		added = true
	}
	l.entry.SetText("")
	if added {
		l.refresh()
		l.changed()
	}
}

func (l *WordList) changed() {
	if l.OnChanged != nil {
		l.OnChanged(l.Words())
	}
}

func (l *WordList) refresh() {
	objs := make([]fyne.CanvasObject, len(l.words))
	for i, w := range l.words {
		remove := widget.NewButtonWithIcon(w, theme.CancelIcon(), func() { l.Remove(w) })
		remove.IconPlacement = widget.ButtonIconTrailingText
		remove.Importance = widget.LowImportance
		objs[i] = remove
	}
	l.chips.Objects = objs
	l.chips.Refresh()
	l.empty.Hidden = len(l.words) > 0 || l.empty.Text == ""
	l.empty.Refresh()
}
