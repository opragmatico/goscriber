package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exec arguments follow the Desktop Entry Specification: reserved characters
// put the argument in quotes, where ", `, $ and \ take a backslash, and the
// string escape then doubles every backslash.
func TestDesktopExecArg(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"/opt/goscriber/bin/goscriber", "/opt/goscriber/bin/goscriber"},
		{"/home/ana/my apps/goscriber", `"/home/ana/my apps/goscriber"`},
		{`/home/ana/it's/goscriber`, `"/home/ana/it's/goscriber"`},
		{`/home/ana/a"b/goscriber`, `"/home/ana/a\\"b/goscriber"`},
		{"/home/ana/$HOME/goscriber", `"/home/ana/\\$HOME/goscriber"`},
		{`/home/ana/a\b/goscriber`, `"/home/ana/a\\\\b/goscriber"`},
		{"/home/ana/100%/goscriber", "/home/ana/100%%/goscriber"},
		{"/home/ana/100% sure/goscriber", `"/home/ana/100%% sure/goscriber"`},
	} {
		got, err := desktopExecArg(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("desktopExecArg(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", "/home/ana/a\nb/goscriber"} {
		if got, err := desktopExecArg(bad); err == nil {
			t.Errorf("desktopExecArg(%q) = %q, want an error", bad, got)
		}
	}
}

func TestAutostartEntry(t *testing.T) {
	entry, err := autostartEntry("/home/ana/my apps/goscriber")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[Desktop Entry]\n",
		"\nType=Application\n",
		"\nExec=\"/home/ana/my apps/goscriber\" --at-login\n",
		"\nTryExec=/home/ana/my apps/goscriber\n",
		"\nIcon=io.github.opragmatico.goscriber\n",
	} {
		if !strings.Contains(entry, want) {
			t.Errorf("the entry lacks %q:\n%s", want, entry)
		}
	}
	if !entryEnabled(entry) {
		t.Error("Goscriber's own entry reads as turned off")
	}
}

// An entry is off when it is hidden or GNOME's startup applications turned
// it off; keys of other groups do not count.
func TestEntryEnabled(t *testing.T) {
	for _, tt := range []struct {
		content string
		want    bool
	}{
		{"[Desktop Entry]\nExec=goscriber\n", true},
		{"[Desktop Entry]\nHidden=true\n", false},
		{"[Desktop Entry]\nHidden = true\n", false},
		{"[Desktop Entry]\nHidden=false\n", true},
		{"[Desktop Entry]\nX-GNOME-Autostart-enabled=false\n", false},
		{"[Desktop Entry]\nX-GNOME-Autostart-enabled=true\n", true},
		{"# Hidden=true\n[Desktop Entry]\nExec=goscriber\n", true},
		{"[Desktop Entry]\nExec=goscriber\n[Desktop Action quit]\nHidden=true\n", true},
	} {
		if got := entryEnabled(tt.content); got != tt.want {
			t.Errorf("entryEnabled(%q) = %v, want %v", tt.content, got, tt.want)
		}
	}
}

// Turning the login item on writes Goscriber's desktop file into the
// autostart directory, creating it; turning it off removes the file. Both
// may be repeated, and an unchanged file is not written again.
func TestXDGAutostart(t *testing.T) {
	x := xdgAutostart{dir: filepath.Join(t.TempDir(), "autostart")}
	if on, err := x.enabled(); err != nil || on {
		t.Fatalf("before: enabled %v, %v", on, err)
	}
	if err := x.set(false, ""); err != nil {
		t.Fatalf("removing an absent login item: %v", err)
	}

	if err := x.set(true, "/opt/goscriber/bin/goscriber"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(x.dir, "io.github.opragmatico.goscriber.desktop")
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "\nExec=/opt/goscriber/bin/goscriber --at-login\n") {
		t.Fatalf("the desktop file: %q, %v", data, err)
	}
	if on, err := x.enabled(); err != nil || !on {
		t.Fatalf("after turning it on: enabled %v, %v", on, err)
	}
	if entries, _ := os.ReadDir(x.dir); len(entries) != 1 {
		t.Errorf("the autostart directory holds %d files, want the desktop file alone", len(entries))
	}

	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := x.set(true, "/opt/goscriber/bin/goscriber"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || !fi.ModTime().Equal(old) {
		t.Error("an unchanged desktop file was written again")
	}
	if err := x.set(true, "/home/ana/goscriber"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "\nExec=/home/ana/goscriber --at-login\n") {
		t.Errorf("the desktop file still starts the old executable: %q", data)
	}

	// Turned off outside Goscriber, then on again from it.
	if err := os.WriteFile(path, []byte("[Desktop Entry]\nExec=x\nX-GNOME-Autostart-enabled=false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if on, _ := x.enabled(); on {
		t.Error("an entry turned off in GNOME's startup applications reads as on")
	}
	if err := x.set(true, "/home/ana/goscriber"); err != nil {
		t.Fatal(err)
	}
	if on, _ := x.enabled(); !on {
		t.Error("turning the login item on again left it off")
	}

	if err := x.set(false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the desktop file is still there: %v", err)
	}
}

func TestWindowsRunCommand(t *testing.T) {
	got := windowsRunCommand(`C:\Program Files\Goscriber\goscriber.exe`)
	if want := `"C:\Program Files\Goscriber\goscriber.exe" --at-login`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUnsupportedErrorMatches(t *testing.T) {
	err := error(unsupportedError("it needs a newer system"))
	if !errors.Is(err, ErrLoginItemUnsupported) || err.Error() != "it needs a newer system" {
		t.Errorf("%v: matches ErrLoginItemUnsupported: %v", err, errors.Is(err, ErrLoginItemUnsupported))
	}
}
