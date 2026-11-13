package platform

import (
	"runtime"
	"strings"
	"testing"
)

func TestPermissionDescriptions(t *testing.T) {
	for _, p := range Permissions {
		if p.Title() == string(p) || p.Why() == "" {
			t.Errorf("%s has no title or explanation", p)
		}
		if u := p.SettingsURL(); !strings.HasPrefix(u, "x-apple.systempreferences:com.apple.preference.security?Privacy_") ||
			strings.HasSuffix(u, "?Privacy_") {
			t.Errorf("%s settings URL %q", p, u)
		}
	}
	if !PermMicrophone.Required() || !PermAccessibility.Required() || PermInputMonitoring.Required() {
		t.Error("the shortcut's active event tap needs Accessibility, not Input Monitoring")
	}
	if got := PermAccessibility.SettingsURL(); got != "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility" {
		t.Errorf("accessibility URL %q", got)
	}
}

func TestPermissionStatus(t *testing.T) {
	for av, want := range map[int]PermissionStatus{0: StatusNotDetermined, 1: StatusRestricted, 2: StatusDenied, 3: StatusGranted} {
		if got := micStatus(av); got != want {
			t.Errorf("AVAuthorizationStatus %d is %v, want %v", av, got, want)
		}
	}
	if !StatusGranted.Usable() || !StatusNotNeeded.Usable() || StatusDenied.Usable() || StatusNotDetermined.Usable() {
		t.Error("usable statuses")
	}
	if StatusRestricted.String() != "restricted" || PermissionStatus(42).String() != "PermissionStatus(42)" {
		t.Error("status names")
	}
	states := PermissionStates()
	if len(states) != len(Permissions) {
		t.Fatalf("states %+v", states)
	}
	if runtime.GOOS != "darwin" {
		for _, s := range states {
			if s.Status != StatusNotNeeded || Request(s.Permission) != StatusNotNeeded {
				t.Errorf("%s: %v, want not needed off macOS", s.Permission, s.Status)
			}
		}
	}
}
