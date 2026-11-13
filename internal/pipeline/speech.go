package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/opragmatico/goscriber/internal/audio"
	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
)

// ErrNoSpeech means a recording held no speech, so there is nothing to
// output. It is an outcome, not a failure: the session ends quietly, with a
// debug log line and no notice. Test for it with errors.Is; the error itself
// is a *NoSpeechError.
var ErrNoSpeech = errors.New("no speech")

// NoSpeechReason says why a recording counts as having no speech.
type NoSpeechReason string

// Reasons for no speech.
const (
	// NoSpeechTooShort: the recording is shorter than the gate's minimum,
	// such as an accidental tap of the shortcut. The model is not called.
	NoSpeechTooShort NoSpeechReason = "too_short"
	// NoSpeechSilent: even the recording's loudest moment is quieter than
	// the gate's threshold. The model is not called.
	NoSpeechSilent NoSpeechReason = "silent"
	// NoSpeechEmptyTranscript: the model heard nothing it could write down,
	// and returned an empty or whitespace-only transcript.
	NoSpeechEmptyTranscript NoSpeechReason = "empty_transcript"
)

// NoSpeechError is ErrNoSpeech with the reason and what the gate measured,
// for the debug log.
type NoSpeechError struct {
	Reason NoSpeechReason
	// Duration is the recording's length.
	Duration time.Duration
	// LoudestDB is the RMS of the recording's loudest 1/audio.LevelRate
	// window, in dBFS, when the gate measured it.
	LoudestDB float64
	// Gate is the gate that dropped the recording.
	Gate SpeechGate
}

func (e *NoSpeechError) Error() string {
	switch e.Reason {
	case NoSpeechTooShort:
		return fmt.Sprintf("no speech: the recording lasts %s, under the %s minimum",
			e.Duration.Round(time.Millisecond), e.Gate.MinDuration)
	case NoSpeechSilent:
		return fmt.Sprintf("no speech: the loudest moment of the recording is %.1f dBFS, under the %.1f dBFS silence threshold",
			e.LoudestDB, audio.DBFS(e.Gate.SilenceThreshold))
	default:
		return fmt.Sprintf("no speech: the model wrote nothing for %s of audio", e.Duration.Round(time.Millisecond))
	}
}

// Is makes errors.Is(err, ErrNoSpeech) true.
func (e *NoSpeechError) Is(target error) bool { return target == ErrNoSpeech }

// SpeechGate drops recordings that cannot hold speech before they reach the
// model, which saves a decode (and possibly a model load) and gives the
// model no chance to hear words in near-silence. Its zero value lets
// everything through except empty recordings.
type SpeechGate struct {
	// MinDuration is the shortest recording worth transcribing. Zero turns
	// the check off.
	MinDuration time.Duration
	// SilenceThreshold is an RMS level from 0 to 1: a recording whose
	// loudest 1/audio.LevelRate window is quieter counts as silent. It
	// looks at the loudest window rather than the whole clip, so a short
	// word in a long recording still passes. Zero turns the check off.
	SilenceThreshold float32
}

// GateFromSettings returns the gate the settings describe.
// silence_threshold_db at -100 dBFS or below turns the silence check off.
func GateFromSettings(s config.Settings) SpeechGate {
	g := SpeechGate{MinDuration: time.Duration(s.MinRecordingMS) * time.Millisecond}
	if s.SilenceThresholdDB > -100 {
		g.SilenceThreshold = float32(math.Pow(10, s.SilenceThresholdDB/20))
	}
	return g
}

// Check returns a *NoSpeechError when samples, mono at rate Hz, should not
// reach the model, and nil when they should. A rate of 0 (the model's own,
// to the engine) leaves the length unknown, so only an empty clip counts as
// too short then.
func (g SpeechGate) Check(samples []float32, rate int) error {
	var dur time.Duration
	if rate > 0 {
		dur = time.Duration(len(samples)) * time.Second / time.Duration(rate)
	}
	if len(samples) == 0 || (rate > 0 && dur < g.MinDuration) {
		return &NoSpeechError{Reason: NoSpeechTooShort, Duration: dur, Gate: g}
	}
	if g.SilenceThreshold > 0 {
		if loud := audio.Loudest(samples, rate); loud.RMS < g.SilenceThreshold {
			return &NoSpeechError{Reason: NoSpeechSilent, Duration: dur, LoudestDB: audio.DBFS(loud.RMS), Gate: g}
		}
	}
	return nil
}

// CheckTranscript returns a *NoSpeechError when a final transcript is empty
// or only whitespace: the model heard no speech, so nothing is output.
func CheckTranscript(r engine.Result) error {
	if strings.TrimSpace(r.Text) != "" {
		return nil
	}
	return &NoSpeechError{Reason: NoSpeechEmptyTranscript, Duration: r.AudioProcessed}
}

// Recognize transcribes a whole recording: it checks the recording against
// gate, decodes it with eng, and checks the transcript. It returns an error
// matching ErrNoSpeech when the recording held no speech, and the engine's
// error when the decode failed.
func Recognize(ctx context.Context, eng engine.Engine, samples []float32, rate int, opts engine.Options, gate SpeechGate) (engine.Result, error) {
	if err := gate.Check(samples, rate); err != nil {
		return engine.Result{}, err
	}
	r, err := eng.Recognize(ctx, samples, rate, opts)
	if err != nil {
		return engine.Result{}, err
	}
	if err := CheckTranscript(r); err != nil {
		return engine.Result{}, err
	}
	return r, nil
}
