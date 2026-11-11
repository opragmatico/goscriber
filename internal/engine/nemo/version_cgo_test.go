//go:build cgo && nemo

package nemo

import (
	"os/exec"
	"strings"
	"testing"
)

// The runtime the test binary loads must be the SDK it was built against; a
// mismatch means the loader found another libnemo_speech_asr_c first.
func TestVersionMatchesSDK(t *testing.T) {
	out, err := exec.Command("pkg-config", "--modversion", "nemo-speech-asr").Output()
	if err != nil {
		t.Fatalf("pkg-config --modversion nemo-speech-asr: %v", err)
	}
	want := strings.TrimSpace(string(out))
	if got := Version(); got != want {
		t.Errorf("Version() = %q, want %q from the SDK's pkg-config file", got, want)
	}
}
