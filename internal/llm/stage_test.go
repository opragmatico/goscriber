package llm

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/pipeline"
)

// The stage post-processes a transcript with llm_enabled, and a dictation
// started with the post-processing shortcut even without it.
func TestStageFollowsTheSettingAndTheShortcut(t *testing.T) {
	f := newFakeServer(t, func(map[string]any) (int, string) { return http.StatusOK, reply("Cleaned.", "stop") })
	var failures []error
	st := &Stage{
		Client: &Client{},
		Failed: func(err error) { failures = append(failures, err) },
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	set := config.Defaults()
	set.LLMBaseURL, set.LLMModel = f.endpoint().BaseURL, "gemma4:26b"
	requests := 0
	for _, tc := range []struct {
		name                 string
		enabled, postProcess bool
		text, want           string
	}{
		{"off", false, false, "raw text", "raw text"},
		{"llm_enabled", true, false, "raw text", "Cleaned."},
		{"post-processing shortcut", false, true, "raw text", "Cleaned."},
		{"both", true, true, "raw text", "Cleaned."},
		{"nothing said", false, true, "  ", "  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set.LLMEnabled = tc.enabled
			got, err := st.Process(context.Background(), tc.text, pipeline.SessionInfo{Session: 1, PostProcess: tc.postProcess, Settings: set})
			if err != nil || got != tc.want {
				t.Errorf("Process = %q, %v; want %q", got, err, tc.want)
			}
			if tc.want != tc.text {
				requests++
			}
			if n := len(f.sent()); n != requests {
				t.Errorf("%d requests, want %d", n, requests)
			}
		})
	}
	if len(failures) > 0 {
		t.Errorf("failures reported: %v", failures)
	}
}
