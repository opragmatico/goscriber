package nemo

import (
	"errors"
	"testing"

	"github.com/opragmatico/goscriber/internal/engine"
)

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]string{
		"nemo-speech-asr 0.3.0":           "0.3.0",
		"nemo-speech-asr 0.4.0-rc.1+g1a2": "0.4.0-rc.1+g1a2",
		"  nemo-speech-asr 0.3.0\n":       "0.3.0",
		"0.3.0":                           "0.3.0",
		"":                                "",
	} {
		if got := parseVersion(in); got != want {
			t.Errorf("parseVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompatible(t *testing.T) {
	for _, tc := range []struct {
		have, want string
		ok         bool
	}{
		{"0.3.0", "0.3.0", true},
		{"0.3.7", "0.3.0", true},
		{"0.3.1-rc.1", "0.3.0", true},
		{"0.4.0", "0.3.0", false}, // 0.x: a minor bump may break
		{"0.2.9", "0.3.0", false},
		{"1.0.0", "0.3.0", false},
		{"1.4.2", "1.0.0", true},
		{"1.4-rc", "1.0.0", true},
		{"2.0.0", "1.9.0", false},
		{"", "0.3.0", false},
		{"nemo-speech-asr 0.3.0", "0.3.0", false}, // unparsed
	} {
		if got := compatible(tc.have, tc.want); got != tc.ok {
			t.Errorf("compatible(%q, %q) = %v, want %v", tc.have, tc.want, got, tc.ok)
		}
	}
}

func TestCheckVersion(t *testing.T) {
	if err := checkVersion(sdkVersion); err != nil {
		t.Errorf("checkVersion(%q) = %v", sdkVersion, err)
	}
	if err := checkVersion("9.0.0"); !errors.Is(err, engine.ErrUnavailable) {
		t.Errorf("checkVersion(9.0.0) = %v, want ErrUnavailable", err)
	}
}
