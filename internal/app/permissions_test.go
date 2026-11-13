package app

import (
	"slices"
	"sync"
	"testing"

	"github.com/opragmatico/goscriber/internal/notice"
	"github.com/opragmatico/goscriber/internal/pipeline"
	"github.com/opragmatico/goscriber/internal/platform"
)

// testPermissions stands in for macOS's permissions in every app test, so
// none shows a prompt.
var testPermissions = &fakePermissions{}

func init() {
	permissionStatus = testPermissions.status
	requestPermission = testPermissions.request
}

type fakePermissions struct {
	mu        sync.Mutex
	states    map[platform.Permission]platform.PermissionStatus
	answers   map[platform.Permission]platform.PermissionStatus // what a request leads to
	requested []platform.Permission
}

func (f *fakePermissions) reset(states, answers map[platform.Permission]platform.PermissionStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states, f.answers, f.requested = states, answers, nil
}

func (f *fakePermissions) set(p platform.Permission, st platform.PermissionStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[p] = st
}

func (f *fakePermissions) status(p platform.Permission) platform.PermissionStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, ok := f.states[p]; ok {
		return st
	}
	return platform.StatusNotNeeded
}

func (f *fakePermissions) request(p platform.Permission) platform.PermissionStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requested = append(f.requested, p)
	if st, ok := f.answers[p]; ok {
		f.states[p] = st
	}
	if st, ok := f.states[p]; ok {
		return st
	}
	return platform.StatusNotNeeded
}

func (f *fakePermissions) asked() []platform.Permission {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requested)
}

type perms = map[platform.Permission]platform.PermissionStatus

func newPermissionShell(t *testing.T, states, answers perms) (*shell, *trayApp, uiQueue) {
	t.Helper()
	testPermissions.reset(states, answers)
	t.Cleanup(func() { testPermissions.reset(nil, nil) })
	return newNoticeShell(t, `{"schema_version": 1, "selected_model": "m"}`)
}

// At launch each missing permission is asked for once; a refusal raises a
// notice with a button to System Settings.
func TestAskPermissionsAtLaunch(t *testing.T) {
	sh, a, q := newPermissionShell(t,
		perms{platform.PermMicrophone: platform.StatusNotDetermined, platform.PermAccessibility: platform.StatusDenied},
		perms{platform.PermMicrophone: platform.StatusDenied})
	sh.trusted = func() bool { return testPermissions.status(platform.PermAccessibility) == platform.StatusGranted }

	sh.askPermissions()
	if got := testPermissions.asked(); !slices.Equal(got, []platform.Permission{platform.PermMicrophone, platform.PermAccessibility}) {
		t.Errorf("asked for %v", got)
	}
	if sh.notices.Count(notice.CodeMicrophoneDenied) != 1 || sh.notices.Count(notice.CodePermissionMissing) != 1 {
		t.Errorf("active notices %+v", sh.notices.Active())
	}
	q.drain()
	b := sh.settings.Banner()
	if b == nil || b.ActionLabel != "Open System Settings" {
		t.Fatalf("banner %+v", b)
	}

	// The microphone notice's button opens its pane.
	sh.noticeAction(notice.ActionOpenMicrophone)
	if len(a.opened) != 1 || a.opened[0] != platform.PermMicrophone.SettingsURL() {
		t.Errorf("opened %v", a.opened)
	}

	// The next launch remembers that Accessibility was asked for; the
	// microphone, once answered, is not asked again either.
	sh.askPermissions()
	if got := testPermissions.asked(); len(got) != 2 {
		t.Errorf("asked again: %v", got)
	}
}

// Granting a permission in System Settings ends its notice.
func TestPermissionNoticesEndWhenGranted(t *testing.T) {
	sh, _, q := newPermissionShell(t, perms{platform.PermMicrophone: platform.StatusDenied}, nil)
	sh.askPermissions()
	if sh.notices.Count(notice.CodeMicrophoneDenied) != 1 {
		t.Fatal("no microphone notice")
	}
	sh.checkPermissions()
	if sh.notices.Count(notice.CodeMicrophoneDenied) != 1 {
		t.Error("the notice should stay while the permission is missing")
	}
	testPermissions.set(platform.PermMicrophone, platform.StatusGranted)
	sh.checkPermissions()
	if sh.notices.Count(notice.CodeMicrophoneDenied) != 0 {
		t.Error("granting the microphone should end the notice")
	}
	q.drain()
}

// Without the microphone permission, dictation records nothing and says
// why.
func TestNoMicrophonePermissionRefusesDictation(t *testing.T) {
	sh, _, q := newPermissionShell(t, perms{platform.PermMicrophone: platform.StatusDenied}, nil)
	rec := sh.svc.recorder.(*testRecorder)
	sh.dictate(pipeline.ActionStart)
	q.waitUI(t, "the notice", func() bool { return sh.notices.Count(notice.CodeMicrophoneDenied) > 0 })
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.starts != 0 {
		t.Errorf("%d recordings without the microphone", rec.starts)
	}
	if microphoneAccess() {
		t.Error("microphoneAccess should refuse a denied permission")
	}
}

func TestPermissionsPage(t *testing.T) {
	sh, a, _ := newPermissionShell(t, perms{
		platform.PermMicrophone:      platform.StatusGranted,
		platform.PermAccessibility:   platform.StatusDenied,
		platform.PermInputMonitoring: platform.StatusDenied,
	}, nil)
	sh.refreshPermissions()
	rows := sh.settings.Permissions()
	if len(rows) != 3 {
		t.Fatalf("rows %+v", rows)
	}
	want := []struct {
		title, status string
		ok, button    bool
	}{
		{"Microphone", "granted", true, false},
		{"Accessibility", "not granted", false, true},
		{"Input Monitoring", "not needed", true, false},
	}
	for i, w := range want {
		r := rows[i]
		if r.Title != w.title || r.Status != w.status || r.OK != w.ok || (r.OnOpen != nil) != w.button {
			t.Errorf("row %d: %+v, want %+v", i, r, w)
		}
	}
	rows[1].OnOpen()
	if len(a.opened) != 1 || a.opened[0] != platform.PermAccessibility.SettingsURL() {
		t.Errorf("opened %v", a.opened)
	}

	// Off macOS there is nothing to show.
	testPermissions.reset(perms{}, nil)
	sh.refreshPermissions()
	if len(sh.settings.Permissions()) != 0 {
		t.Error("no rows when no permission applies")
	}
}

func TestPermissionTrayAlert(t *testing.T) {
	sh, a, q := newPermissionShell(t, perms{platform.PermMicrophone: platform.StatusDenied}, nil)
	sh.askPermissions()
	q.drain()
	alert := sh.tray.Alert()
	if alert == nil || alert.Label != "⚠ Microphone access needed…" {
		t.Fatalf("alert %+v", alert)
	}
	alert.Action()
	if len(a.opened) != 1 || a.opened[0] != platform.PermMicrophone.SettingsURL() {
		t.Errorf("opened %v", a.opened)
	}
}
