//go:build darwin

package platform

import "testing"

func TestProcessName(t *testing.T) {
	for in, want := range map[string]string{
		"/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal\n": "Terminal",
		"loginwindow": "loginwindow",
		"  ":          "",
	} {
		if got := processName(in); got != want {
			t.Errorf("processName(%q) = %q, want %q", in, got, want)
		}
	}
}

// SecureInputEnabled calls into Carbon; it must not crash whatever the
// answer.
func TestSecureInputEnabledRuns(t *testing.T) {
	t.Logf("Secure Event Input is on: %v", SecureInputEnabled())
}
