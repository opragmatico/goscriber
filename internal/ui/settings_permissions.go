package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// PermissionRow is one permission on the Permissions page.
type PermissionRow struct {
	Title  string
	Why    string
	Status string // such as "granted" or "denied"
	// OK is true when the permission lets Goscriber work.
	OK bool
	// OnOpen opens System Settings at the permission; nil shows no button.
	OnOpen func()
}

// PermissionsNone is what the Permissions page says on a system without
// permissions to grant.
const PermissionsNone = "Goscriber needs no special permissions on this system."

// Permissions are the rows of the Permissions page.
func (w *SettingsWindow) Permissions() []PermissionRow { return w.permissions }

// SetPermissions shows rows on the Permissions page; none says no
// permission is needed.
func (w *SettingsWindow) SetPermissions(rows []PermissionRow) {
	w.permissions = rows
	if w.permBox != nil {
		w.permBox.Objects = w.permissionRows()
		w.permBox.Refresh()
	}
}

func (w *SettingsWindow) permissionsPage() fyne.CanvasObject {
	w.permBox = container.NewVBox(w.permissionRows()...)
	return settingsPage("Permissions", w.permBox)
}

func (w *SettingsWindow) permissionRows() []fyne.CanvasObject {
	if len(w.permissions) == 0 {
		return []fyne.CanvasObject{paragraph(PermissionsNone)}
	}
	var out []fyne.CanvasObject
	for _, r := range w.permissions {
		mark := "✓"
		if !r.OK {
			mark = "✗"
		}
		head := widget.NewLabelWithStyle(mark+" "+r.Title+": "+r.Status, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		row := container.NewVBox(head, paragraph(r.Why))
		if r.OnOpen != nil {
			row.Add(container.NewHBox(widget.NewButton("Open System Settings", r.OnOpen)))
		}
		out = append(out, row)
	}
	return out
}
