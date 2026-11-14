package pipeline

import (
	"context"
	"time"
)

// TextStage is an optional step between recognition and output, such as
// filler-word removal (#27), LLM post-processing (#39) or the transcription
// hook (#38). It receives the text so far and returns the text to pass on.
//
// A stage that fails returns an error: the coordinator logs it and passes
// its input on unchanged, so a broken stage never costs the user the
// transcript. A stage that wants the user to hear about a failure publishes
// its own notice. Empty output means nothing is pasted.
type TextStage interface {
	Process(ctx context.Context, text string, info SessionInfo) (string, error)
}

// TextStageFunc adapts a function to a TextStage.
type TextStageFunc func(ctx context.Context, text string, info SessionInfo) (string, error)

// Process implements TextStage.
func (f TextStageFunc) Process(ctx context.Context, text string, info SessionInfo) (string, error) {
	return f(ctx, text, info)
}

// HistorySink keeps finished dictations, for the history page (#32). Save
// runs after the transcript is out, off the dictation's critical path; an
// error is logged.
type HistorySink interface {
	Save(ctx context.Context, e HistoryEntry) error
}

// HistorySinkFunc adapts a function to a HistorySink.
type HistorySinkFunc func(ctx context.Context, e HistoryEntry) error

// Save implements HistorySink.
func (f HistorySinkFunc) Save(ctx context.Context, e HistoryEntry) error { return f(ctx, e) }

// Stages are the pluggable steps of a session. A nil stage is skipped, which
// is how each one starts until its issue lands.
type Stages struct {
	// Cleanup tidies the transcript: filler words, spacing (#27).
	Cleanup TextStage
	// LLM post-processes the transcript with a language model (#39).
	LLM TextStage
	// Hook runs the user's transcription hook (#38).
	Hook TextStage
	// History saves the dictation (#32).
	History HistorySink
}

// SessionInfo describes the session a stage works on.
type SessionInfo struct {
	// Session is the session's ID.
	Session uint64
	// Model is the catalog ID of the model that transcribed it.
	Model string
	// Language is the language the model was asked for: a locale such as
	// "es-ES", "auto", or "" for a model whose language is fixed.
	Language string
	// Languages are the languages the model selected or detected, such as
	// "es-US" when asked for "auto" or even "es-ES". Empty when the model
	// reports none.
	Languages []string
	// Duration is how long the recording lasted.
	Duration time.Duration
}

// HistoryEntry is a finished dictation.
type HistoryEntry struct {
	SessionInfo
	// At is when recording started.
	At time.Time
	// RawText is the model's transcript, before the text stages.
	RawText string
	// Text is what was delivered.
	Text string
	// Samples are the recording, mono at SampleRate Hz. The entry owns them.
	Samples    []float32
	SampleRate int
	// Delivered reports whether Text reached the focused app. It is false
	// when the delivery failed.
	Delivered bool
}
