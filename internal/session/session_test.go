package session

import "testing"

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		goos string
		vars map[string]string
		want Kind
	}{
		{"macOS", "darwin", map[string]string{"DISPLAY": ":0"}, Native},
		{"Windows", "windows", nil, Native},
		{"GNOME on Wayland", "linux", map[string]string{"XDG_SESSION_TYPE": "wayland", "WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"}, Wayland},
		{"X11 session", "linux", map[string]string{"XDG_SESSION_TYPE": "x11", "DISPLAY": ":0"}, X11},
		{"session type wins over a stray WAYLAND_DISPLAY", "linux", map[string]string{"XDG_SESSION_TYPE": "X11", "WAYLAND_DISPLAY": "wayland-1", "DISPLAY": ":0"}, X11},
		{"no session type, Wayland socket", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":1"}, Wayland},
		{"no session type, X display", "linux", map[string]string{"DISPLAY": ":0"}, X11},
		{"tty session type falls back to the variables", "linux", map[string]string{"XDG_SESSION_TYPE": "tty", "DISPLAY": ":0"}, X11},
		{"nothing", "linux", nil, None},
		{"FreeBSD X11", "freebsd", map[string]string{"DISPLAY": ":0"}, X11},
	}
	for _, tt := range tests {
		if got := Detect(tt.goos, env(tt.vars)); got != tt.want {
			t.Errorf("%s: Detect = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestDesktop(t *testing.T) {
	for in, want := range map[string]string{
		"":             "",
		"GNOME":        "GNOME",
		"ubuntu:GNOME": "GNOME",
		"KDE":          "KDE",
		"Hyprland:":    "Hyprland",
	} {
		if got := Desktop(env(map[string]string{"XDG_CURRENT_DESKTOP": in})); got != want {
			t.Errorf("Desktop(%q) = %q, want %q", in, got, want)
		}
	}
}
