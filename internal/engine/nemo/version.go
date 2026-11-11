package nemo

import "strings"

// versionPrefix starts the string nemo_speech_asr_version returns, as in
// "nemo-speech-asr 0.3.0".
const versionPrefix = "nemo-speech-asr "

// parseVersion returns the version number in a nemo_speech_asr_version
// string, or the whole string when it has another form.
func parseVersion(s string) string {
	s = strings.TrimSpace(s)
	if v, ok := strings.CutPrefix(s, versionPrefix); ok {
		return strings.TrimSpace(v)
	}
	return s
}
