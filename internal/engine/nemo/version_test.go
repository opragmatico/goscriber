package nemo

import "testing"

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
