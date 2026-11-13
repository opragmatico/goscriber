package app

import (
	"net/url"

	"github.com/opragmatico/goscriber/internal/hotkey"
	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/platform"
	"github.com/opragmatico/goscriber/internal/ui"
)

// permissionStatus and requestPermission reach the macOS privacy
// permissions; tests replace them.
var (
	permissionStatus  = platform.StatusOf
	requestPermission = platform.Request
)

// askedAccessibility is the app preference that remembers that Goscriber has
// shown macOS's Accessibility prompt, so later launches leave it to the
// notice's button instead of prompting again.
const askedAccessibility = "permissions.asked_accessibility"

// askPermissions asks at launch for what Goscriber needs and does not have:
// the microphone, which macOS prompts for only while it is not determined,
// and Accessibility, prompted for once. What is refused raises a notice whose
// button opens System Settings at the right pane. It runs in its own
// goroutine, not waited for at shutdown: the microphone prompt waits for the
// user.
func (sh *shell) askPermissions() {
	switch st := permissionStatus(platform.PermMicrophone); st {
	case platform.StatusNotDetermined:
		sh.rt.Log.Info("asking for the microphone permission")
		if st := requestPermission(platform.PermMicrophone); !st.Usable() {
			sh.microphoneRefused(st)
		}
	case platform.StatusDenied, platform.StatusRestricted:
		sh.microphoneRefused(st)
	}

	if !sh.trusted() {
		if prefs := sh.app.Preferences(); !prefs.Bool(askedAccessibility) {
			prefs.SetBool(askedAccessibility, true)
			sh.rt.Log.Info("asking for the Accessibility permission")
			requestPermission(platform.PermAccessibility)
		}
		if !sh.trusted() {
			sh.notices.Publish(notice.AccessibilityMissing())
		}
	}
	sh.permissionRows.post(sh.refreshPermissions)
}

func (sh *shell) microphoneRefused(st platform.PermissionStatus) {
	sh.rt.Log.Warn("the microphone permission is missing, so recordings would be silent", "status", st.String())
	sh.notices.Publish(notice.MicrophoneDenied())
}

// microphoneAccess tells the coordinator whether it may record. A
// permission not asked for yet lets the recording go ahead: opening the
// microphone shows macOS's prompt, and the launch already asked.
func microphoneAccess() bool {
	st := permissionStatus(platform.PermMicrophone)
	return st != platform.StatusDenied && st != platform.StatusRestricted
}

// checkPermissions runs on every tick of the permission watcher: it ends
// the notices of permissions granted since, and registers the shortcut
// again once Accessibility is.
func (sh *shell) checkPermissions() {
	if sh.notices.Count(notice.CodePermissionMissing) > 0 && sh.trusted() {
		sh.rt.Log.Info("the Accessibility permission was granted; registering the shortcut again")
		sh.notices.Resolve(notice.CodePermissionMissing)
		sh.hotkeys.Apply(hotkey.ConfigFrom(sh.rt.Settings.Get()))
	}
	if sh.notices.Count(notice.CodeMicrophoneDenied) > 0 && permissionStatus(platform.PermMicrophone).Usable() {
		sh.rt.Log.Info("the microphone permission was granted")
		sh.notices.Resolve(notice.CodeMicrophoneDenied)
	}
	sh.permissionRows.post(func() {
		if sh.settings.Visible() {
			sh.refreshPermissions()
		}
	})
}

// refreshPermissions shows where each permission stands on the settings
// window's Permissions page, on the main thread. A permission Goscriber
// needs and lacks gets a button to System Settings.
func (sh *shell) refreshPermissions() {
	var rows []ui.PermissionRow
	for _, p := range platform.Permissions {
		st := permissionStatus(p)
		if st == platform.StatusNotNeeded {
			continue
		}
		row := ui.PermissionRow{Title: p.Title(), Why: p.Why(), Status: st.String(), OK: st.Usable()}
		switch {
		case !p.Required() && !st.Usable():
			row.Status, row.OK = "not needed", true
		case !st.Usable():
			u := p.SettingsURL()
			row.OnOpen = func() { sh.openURL(u) }
		}
		rows = append(rows, row)
	}
	sh.settings.SetPermissions(rows)
}

// openURL opens a link, such as a System Settings pane.
func (sh *shell) openURL(link string) {
	u, err := url.Parse(link)
	if err == nil {
		err = sh.app.OpenURL(u)
	}
	if err != nil {
		sh.rt.Log.Warn("could not open a link", "url", link, "err", err)
	}
}
