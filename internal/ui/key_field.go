package ui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Labels of a keyField's buttons, which tests look for.
const (
	LabelSaveKey   = "Save"
	LabelRemoveKey = "Remove"
)

// keyHints are what a keyField's note says.
type keyHints struct {
	stored    string // a secret is stored for the account shown
	none      string // none is
	unknown   string // the keychain could not be read
	noAccount string // there is no account to store one for
}

// keyField takes a secret the system keychain keeps, such as an API key or
// a password, for the account the page shows: a password field with Save
// and Remove, and a note on whether one is stored. The stored secret is
// never shown; the field takes a new one.
type keyField struct {
	w     *SettingsWindow
	has   func(account string) (bool, error)
	set   func(account, secret string) error
	hints keyHints

	entry  *widget.Entry
	note   *widget.Label
	save   *widget.Button
	remove *widget.Button
	box    fyne.CanvasObject

	account string // the account shown, or ""
	checked string // the account the note is about, or "" to read it again
}

// newKeyField makes a keyField that asks has whether a secret is stored for
// an account and stores one with set ("" removes it). A nil has leaves the
// note empty.
func newKeyField(w *SettingsWindow, placeholder string, hints keyHints, has func(string) (bool, error), set func(string, string) error) *keyField {
	k := &keyField{w: w, has: has, set: set, hints: hints}
	k.entry = widget.NewPasswordEntry()
	k.entry.SetPlaceHolder(placeholder)
	k.entry.OnSubmitted = func(string) { k.saveTyped() }
	k.note = note("")
	k.save = widget.NewButton(LabelSaveKey, k.saveTyped)
	k.remove = widget.NewButtonWithIcon(LabelRemoveKey, theme.DeleteIcon(), func() { k.store("") })
	k.remove.Disable()
	k.box = container.NewVBox(
		container.NewBorder(nil, nil, nil, container.NewHBox(k.save, k.remove), k.entry),
		k.note,
	)
	return k
}

// saveTyped stores what the field holds.
func (k *keyField) saveTyped() {
	secret := strings.TrimSpace(k.entry.Text)
	if secret == "" || k.account == "" {
		return
	}
	k.store(secret)
}

// store stores secret for the account shown, or removes it with "", in the
// background.
func (k *keyField) store(secret string) {
	account := k.account
	go func() {
		err := k.set(account, secret)
		k.w.do(func() {
			if err != nil {
				k.w.log().Warn("the secret could not be stored in the keychain", "err", err)
				k.note.SetText(fmt.Sprintf("Could not store it: %v", err))
				return
			}
			k.entry.SetText("")
			k.checked = ""
			k.show(account)
		})
	}()
}

// show follows the account the page shows: it says whether a secret is
// stored for it, reading the keychain in the background. "" has no account,
// so nothing can be stored.
func (k *keyField) show(account string) {
	k.account = account
	if account == "" {
		k.checked = ""
		k.note.SetText(k.hints.noAccount)
		k.entry.Disable()
		k.save.Disable()
		k.remove.Disable()
		return
	}
	k.entry.Enable()
	k.save.Enable()
	if k.has == nil || k.checked == account {
		return
	}
	k.checked = account
	has := k.has
	go func() {
		stored, err := has(account)
		k.w.do(func() {
			if k.checked != account {
				return
			}
			switch {
			case err != nil:
				k.w.log().Warn("the keychain could not be read", "err", err)
				k.note.SetText(k.hints.unknown)
			case stored:
				k.note.SetText(k.hints.stored)
			default:
				k.note.SetText(k.hints.none)
			}
			if stored {
				k.remove.Enable()
			} else {
				k.remove.Disable()
			}
		})
	}()
}

// setHints changes what the note says, and reads the keychain again to say
// it.
func (k *keyField) setHints(h keyHints) {
	if k.hints == h {
		return
	}
	k.hints = h
	k.checked = ""
	k.show(k.account)
}
