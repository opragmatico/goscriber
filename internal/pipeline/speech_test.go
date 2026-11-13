package pipeline

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/fake"
)

const rate = 16000

// tone is d of a 220 Hz sine at amp, standing in for speech.
func tone(d time.Duration, amp float64) []float32 {
	p := make([]float32, int(d*rate/time.Second))
	for i := range p {
		p[i] = float32(amp * math.Sin(2*math.Pi*220*float64(i)/rate))
	}
	return p
}

// noise is d of seeded white noise at an RMS of db dBFS.
func noise(d time.Duration, db float64) []float32 {
	rng := rand.New(rand.NewPCG(1, 2))
	amp := math.Pow(10, db/20) * math.Sqrt(3) // uniform noise's RMS is amp/√3
	p := make([]float32, int(d*rate/time.Second))
	for i := range p {
		p[i] = float32(amp * (rng.Float64()*2 - 1))
	}
	return p
}

func loaded(t *testing.T, script fake.Script) *fake.Engine {
	t.Helper()
	e := fake.New(script)
	if _, err := e.Load(context.Background(), engine.LoadSpec{ID: "asr", Path: "asr.gguf"}); err != nil {
		t.Fatal(err)
	}
	return e
}

func wantNoSpeech(t *testing.T, err error, reason NoSpeechReason) *NoSpeechError {
	t.Helper()
	if !errors.Is(err, ErrNoSpeech) {
		t.Fatalf("err = %v, want ErrNoSpeech", err)
	}
	ns, ok := errors.AsType[*NoSpeechError](err)
	if !ok || ns.Reason != reason {
		t.Fatalf("err = %#v, want reason %s", err, reason)
	}
	return ns
}

func TestGateFromSettings(t *testing.T) {
	g := GateFromSettings(config.Defaults())
	if g.MinDuration != 250*time.Millisecond || math.Abs(float64(g.SilenceThreshold)-0.001) > 1e-6 {
		t.Errorf("default gate %+v, want 250 ms and -60 dBFS (0.001)", g)
	}
	s := config.Defaults()
	s.MinRecordingMS, s.SilenceThresholdDB = 0, -100
	if g := GateFromSettings(s); g != (SpeechGate{}) {
		t.Errorf("gate %+v, want both checks off", g)
	}
}

func TestSpeechGate(t *testing.T) {
	gate := GateFromSettings(config.Defaults())
	// A word in the middle of four seconds of near-silence.
	word := noise(4*time.Second, -75)
	copy(word[2*rate:], tone(300*time.Millisecond, 0.05))

	for _, tc := range []struct {
		name    string
		gate    SpeechGate
		samples []float32
		reason  NoSpeechReason // "" means it passes
	}{
		{"speech", gate, tone(time.Second, 0.1), ""},
		{"short word in long silence", gate, word, ""},
		{"empty", SpeechGate{}, nil, NoSpeechTooShort},
		{"accidental tap", gate, tone(200*time.Millisecond, 0.1), NoSpeechTooShort},
		{"exactly the minimum", gate, tone(250*time.Millisecond, 0.1), ""},
		{"digital silence", gate, make([]float32, 2*rate), NoSpeechSilent},
		{"quiet room", gate, noise(5*time.Second, -70), NoSpeechSilent},
		{"room noise above the threshold reaches the model", gate, noise(5*time.Second, -45), ""},
		{"checks off", SpeechGate{}, make([]float32, 100), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.gate.Check(tc.samples, rate)
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("err = %v, want the recording to pass", err)
				}
				return
			}
			ns := wantNoSpeech(t, err, tc.reason)
			if want := time.Duration(len(tc.samples)) * time.Second / rate; ns.Duration != want {
				t.Errorf("Duration %s, want %s", ns.Duration, want)
			}
		})
	}
}

func TestNoSpeechMessages(t *testing.T) {
	gate := GateFromSettings(config.Defaults())
	for _, tc := range []struct {
		err  error
		want string
	}{
		{gate.Check(tone(120*time.Millisecond, 0.1), rate), "lasts 120ms, under the 250ms minimum"},
		{gate.Check(noise(time.Second, -70), rate), "dBFS, under the -60.0 dBFS silence threshold"},
		{CheckTranscript(engine.Result{AudioProcessed: 2 * time.Second}), "the model wrote nothing for 2s of audio"},
	} {
		if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
			t.Errorf("message %q, want it to contain %q", tc.err, tc.want)
		}
	}
}

func TestRecognize(t *testing.T) {
	gate := GateFromSettings(config.Defaults())
	speech := tone(time.Second, 0.1)

	t.Run("speech", func(t *testing.T) {
		e := loaded(t, fake.Script{Replies: []fake.Reply{fake.Transcript("Ask not.")}})
		r, err := Recognize(context.Background(), e, speech, rate, engine.Options{Language: "en-US"}, gate)
		if err != nil || r.Text != "Ask not." {
			t.Fatalf("Recognize = %+v, %v", r, err)
		}
		if recs := e.Recognitions(); len(recs) != 1 || recs[0].Samples != len(speech) || recs[0].Options.Language != "en-US" {
			t.Errorf("recognitions %+v", recs)
		}
	})

	for _, text := range []string{"", "   ", "\n\t "} {
		t.Run("empty transcript "+strings.ReplaceAll(text, "\n", `\n`), func(t *testing.T) {
			e := loaded(t, fake.Script{Replies: []fake.Reply{fake.Transcript(text)}})
			_, err := Recognize(context.Background(), e, speech, rate, engine.Options{}, gate)
			ns := wantNoSpeech(t, err, NoSpeechEmptyTranscript)
			if ns.Duration != time.Second {
				t.Errorf("Duration %s, want the audio the model processed", ns.Duration)
			}
		})
	}

	for name, samples := range map[string][]float32{
		"too short": tone(100*time.Millisecond, 0.1),
		"silent":    make([]float32, 5*rate),
	} {
		t.Run(name+" skips the model", func(t *testing.T) {
			e := loaded(t, fake.Script{Replies: []fake.Reply{fake.Transcript("Yeah.")}})
			if _, err := Recognize(context.Background(), e, samples, rate, engine.Options{}, gate); !errors.Is(err, ErrNoSpeech) {
				t.Fatalf("err = %v, want ErrNoSpeech", err)
			}
			if n := len(e.Recognitions()); n != 0 {
				t.Errorf("the model was called %d times", n)
			}
		})
	}

	t.Run("engine failure is not no speech", func(t *testing.T) {
		e := loaded(t, fake.Script{Replies: []fake.Reply{{Err: engine.ErrRuntime}}})
		_, err := Recognize(context.Background(), e, speech, rate, engine.Options{}, gate)
		if !errors.Is(err, engine.ErrRuntime) || errors.Is(err, ErrNoSpeech) {
			t.Fatalf("err = %v, want the engine's ErrRuntime", err)
		}
	})
}

// A rate of 0 means the model's own rate to the engine: the length is
// unknown, so the minimum does not apply, but loudness still does.
func TestSpeechGateWithoutRate(t *testing.T) {
	gate := GateFromSettings(config.Defaults())
	if err := gate.Check(tone(100*time.Millisecond, 0.1), 0); err != nil {
		t.Errorf("short clip at rate 0: %v", err)
	}
	_ = wantNoSpeech(t, gate.Check(make([]float32, rate), 0), NoSpeechSilent)
	_ = wantNoSpeech(t, gate.Check(nil, 0), NoSpeechTooShort)
}
