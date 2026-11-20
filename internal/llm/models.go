package llm

import (
	"fmt"
	"strings"
)

// Models Goscriber suggests for a local provider, by the computer's memory
// (docs/llm-post-processing.md#models): gemma4:26b fixed 25 to 27 of the
// test talk's 31 errors, and gemma4:e4b, a 9.5 GB download against 16 GB,
// 20.
const (
	LargeModel = "gemma4:26b"
	SmallModel = "gemma4:e4b"
)

// largeModelMemory is the least memory LargeModel is suggested for: 32 GB,
// less the part a Linux kernel keeps for itself and leaves out of the
// total it reports.
const largeModelMemory = 30 << 30

// SuggestedModel is the model suggested for a computer with memory bytes
// of memory, 0 when it is not known.
func SuggestedModel(memory uint64) string {
	if memory >= largeModelMemory {
		return LargeModel
	}
	return SmallModel
}

// ModelFor is the model a job sends to: model when set, else, for a local
// provider, the one suggested for memory. A hosted provider has no
// default: its model must be chosen.
func ModelFor(baseURL, model string, memory uint64) (string, error) {
	switch {
	case model != "":
		return model, nil
	case IsLocal(baseURL):
		return SuggestedModel(memory), nil
	default:
		return "", fmt.Errorf("no model is chosen for %s", baseURL)
	}
}

// FormatMemory writes memory in whole gigabytes, as "36 GB".
func FormatMemory(memory uint64) string {
	return fmt.Sprintf("%d GB", (memory+(1<<29))>>30)
}

// KeychainService is the name the OS keychain shows the API keys of LLM
// providers under, each under its base URL (KeyAccount).
const KeychainService = "Goscriber LLM API key"

// KeyAccount is the keychain account a base URL's API key is stored
// under: the URL itself, without a trailing slash.
func KeyAccount(baseURL string) string { return strings.TrimRight(strings.TrimSpace(baseURL), "/") }
