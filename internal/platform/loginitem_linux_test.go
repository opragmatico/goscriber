//go:build linux

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// On Linux the login item is a desktop file in $XDG_CONFIG_HOME/autostart
// that starts the running executable.
func TestLoginItemInXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	if err := LoginItemSupported(); err != nil {
		t.Fatal(err)
	}
	if err := SetLoginItem(true); err != nil {
		t.Fatal(err)
	}
	exe, err := executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "autostart", "io.github.opragmatico.goscriber.desktop"))
	if err != nil || !strings.Contains(string(data), "\nTryExec="+exe+"\n") {
		t.Fatalf("the desktop file does not start %s: %q, %v", exe, data, err)
	}
	if on, err := LoginItemEnabled(); err != nil || !on {
		t.Errorf("after SetLoginItem(true): enabled %v, %v", on, err)
	}
	if err := SetLoginItem(false); err != nil {
		t.Fatal(err)
	}
	if on, err := LoginItemEnabled(); err != nil || on {
		t.Errorf("after SetLoginItem(false): enabled %v, %v", on, err)
	}
}
