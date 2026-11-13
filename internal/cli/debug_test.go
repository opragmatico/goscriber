package cli

import (
	"strings"
	"testing"
)

// The recording itself is tested in internal/audio against synthetic and
// null devices; these tests cover the command line around it.
func TestDebugUsage(t *testing.T) {
	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"debug"}, 2, "goscriber debug record"},
		{[]string{"debug", "nope"}, 2, `unknown command "nope"`},
		{[]string{"debug", "record"}, 2, "want one FILE.wav"},
		{[]string{"debug", "record", "a.wav", "b.wav"}, 2, "want one FILE.wav"},
		{[]string{"debug", "record", "--seconds", "0", "a.wav"}, 2, "--seconds must be"},
		{[]string{"debug", "record", "--seconds", "x", "a.wav"}, 2, "invalid value"},
		{[]string{"debug", "devices", "extra"}, 2, "unexpected argument"},
		{[]string{"debug", "sounds", "extra"}, 2, "unexpected argument"},
		{[]string{"debug", "sounds", "--volume", "2"}, 2, "--volume must be"},
	} {
		code, stdout, stderr := runWith(t, Env{}, tt.args...)
		if code != tt.code || stdout != "" || !strings.Contains(stderr, tt.want) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", tt.args, code, stdout, stderr)
		}
	}
	if code, stdout, _ := runWith(t, Env{}, "debug", "--help"); code != 0 || !strings.Contains(stdout, "--always-on") {
		t.Errorf("debug --help: exit %d, %q", code, stdout)
	}
}

func TestMeterBar(t *testing.T) {
	for rms, want := range map[float32]int{0: 0, 1: 30, 0.001: 0, 0.032: 15} {
		bar := meterBar(rms)
		if len(bar) != 32 || strings.Count(bar, "#") != want {
			t.Errorf("meterBar(%v) = %q, want %d marks", rms, bar, want)
		}
	}
}
