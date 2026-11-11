package nemo

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/opragmatico/goscriber/internal/engine"
)

// sdkVersion is the NeMo-Speech.cpp release the binding is written against:
// the submodule pin in third_party/nemo-speech. Update it with the pin.
const sdkVersion = "0.3.0"

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

// checkVersion fails with engine.ErrUnavailable when runtime version have is
// not compatible with sdkVersion.
func checkVersion(have string) error {
	if compatible(have, sdkVersion) {
		return nil
	}
	return &engine.Error{
		Op: "load", Kind: engine.ErrUnavailable,
		Detail: fmt.Sprintf("NeMo-Speech.cpp %s is not compatible with %s, the version Goscriber was built for", have, sdkVersion),
	}
}

// compatible reports whether runtime version have can serve a binding
// written for version want: the same major version, and while that is 0 the
// same minor version too, since 0.x releases may break compatibility in a
// minor bump (semantic versioning's "^0.3.0").
func compatible(have, want string) bool {
	hMajor, hMinor, ok1 := majorMinor(have)
	wMajor, wMinor, ok2 := majorMinor(want)
	if !ok1 || !ok2 || hMajor != wMajor {
		return false
	}
	return wMajor != 0 || hMinor == wMinor
}

// majorMinor parses the leading "MAJOR.MINOR" of a version such as
// "0.4.0-rc.1+g1a2".
func majorMinor(v string) (major, minor int, ok bool) {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(leadingDigits(parts[1]))
	return major, minor, err1 == nil && err2 == nil
}

// leadingDigits returns the digits s starts with, as "2" for "2-rc".
func leadingDigits(s string) string {
	end := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		return s
	}
	return s[:end]
}
