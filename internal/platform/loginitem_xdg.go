package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// xdgAutostart is the Linux login item: a desktop file in the user's XDG
// autostart directory, which desktops following the XDG Autostart
// specification (GNOME, KDE Plasma, Xfce, Cinnamon, MATE, LXQt) start at
// login. The file is Goscriber's own, written and removed whole.
//
// It has no build tag, so its tests run on every system.
type xdgAutostart struct {
	dir string // the autostart directory, ~/.config/autostart by default
}

func (x xdgAutostart) path() string { return filepath.Join(x.dir, loginItemID+".desktop") }

// enabled reports whether the desktop starts Goscriber at login: the file is
// there, and neither hidden (the specification's way to turn an entry off,
// which Xfce's session settings use) nor turned off in GNOME's startup
// applications (X-GNOME-Autostart-enabled=false).
func (x xdgAutostart) enabled() (bool, error) {
	data, err := os.ReadFile(x.path())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("platform: read the login item: %w", err)
	}
	return entryEnabled(string(data)), nil
}

// set writes the desktop file to start exe at login, or removes it. A file
// that already says the same is left alone.
func (x xdgAutostart) set(on bool, exe string) error {
	if !on {
		if err := os.Remove(x.path()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("platform: remove the login item: %w", err)
		}
		return nil
	}
	entry, err := autostartEntry(exe)
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(x.path()); err == nil && string(old) == entry {
		return nil
	}
	if err := os.MkdirAll(x.dir, 0o700); err != nil {
		return fmt.Errorf("platform: write the login item: %w", err)
	}
	if err := writeFileAtomic(x.path(), []byte(entry)); err != nil {
		return fmt.Errorf("platform: write the login item: %w", err)
	}
	return nil
}

// autostartEntry is the desktop file that starts exe at login, hidden.
// TryExec makes the desktop skip it once exe is gone.
func autostartEntry(exe string) (string, error) {
	arg, err := desktopExecArg(exe)
	if err != nil {
		return "", err
	}
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=Goscriber\n" +
		"Comment=Offline push-to-talk speech-to-text\n" +
		"Icon=" + loginItemID + "\n" +
		"Exec=" + arg + " " + AtLoginFlag + "\n" +
		"TryExec=" + strings.ReplaceAll(exe, `\`, `\\`) + "\n" +
		"Terminal=false\n" +
		"StartupNotify=false\n" +
		"X-GNOME-Autostart-enabled=true\n", nil
}

// desktopExecArg writes s as one argument of a desktop file's Exec key, by
// the Desktop Entry Specification. An argument with a reserved character
// goes in double quotes, inside which ", `, $ and \ take a backslash; then,
// because Exec is a string, every backslash is doubled; and % is doubled,
// since it starts a field code. A control character cannot be written.
func desktopExecArg(s string) (string, error) {
	if s == "" || strings.ContainsFunc(s, unicode.IsControl) {
		return "", fmt.Errorf("platform: %q cannot be started from a desktop file", s)
	}
	if strings.ContainsAny(s, " \t\"'\\><~|&;$*?#()`") {
		var b strings.Builder
		b.WriteByte('"')
		for _, r := range s {
			if strings.ContainsRune("\"`$\\", r) {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		b.WriteByte('"')
		s = b.String()
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "%", "%%"), nil
}

// entryEnabled reads the [Desktop Entry] group of an autostart file:
// Hidden=true means the entry is deleted, and X-GNOME-Autostart-enabled=false
// that it is turned off.
func entryEnabled(content string) bool {
	group := ""
	for line := range strings.Lines(content) {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "["):
			group = line
		case group == "[Desktop Entry]":
			key, value, _ := strings.Cut(line, "=")
			key, value = strings.TrimSpace(key), strings.TrimSpace(value)
			if key == "Hidden" && value == "true" || key == "X-GNOME-Autostart-enabled" && value == "false" {
				return false
			}
		}
	}
	return true
}

// writeFileAtomic writes data to path through a temporary file renamed over
// it, so the desktop never reads half a file. The temporary file's name does
// not end in .desktop, so no desktop takes it for an entry meanwhile.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
