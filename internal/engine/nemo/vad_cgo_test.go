//go:build cgo && nemo

package nemo

import (
	"context"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/opragmatico/goscriber/internal/engine"
)

// vadFile is the Silero VAD GGUF the gated VAD tests load from
// GOSCRIBER_TEST_MODELS, as `goscriber models pull` downloads it.
const vadFile = "silero-vad-v6.2.0.gguf"

// speechModels are the ASR models the VAD tests run on, when present.
var speechModels = []string{nemotronFile, "parakeet-tdt-0.6b-v2.q8_0.gguf"}

// The thresholds the settings default to, passed explicitly so the test
// covers the plumbing to the library.
const testOnset, testOffset = 0.5, 0.3

// loadWithVAD loads file with the VAD, skipping the test when either GGUF is
// missing.
func loadWithVAD(t *testing.T, file string) (*Engine, engine.ModelInfo) {
	t.Helper()
	vad := testModel(t, vadFile)
	path := testModel(t, file)
	e := New()
	t.Cleanup(func() { _ = e.Unload() })
	info, err := e.Load(context.Background(), engine.LoadSpec{
		ID: file, Path: path, VADPath: vad, VADOnset: testOnset, VADOffset: testOffset,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e, info
}

// padSilence surrounds samples with d of digital silence on each side.
func padSilence(samples []float32, rate int, d time.Duration) []float32 {
	n := int(d * time.Duration(rate) / time.Second)
	out := make([]float32, n+len(samples)+n)
	copy(out[n:], samples)
	return out
}

// pinkNoise returns d of seeded pink noise (Paul Kellet's filter over white
// noise) at rms, which stands in for the hiss and hum of a quiet room.
func pinkNoise(rate int, d time.Duration, rms float64, seed uint64) []float32 {
	rng := rand.New(rand.NewPCG(seed, seed))
	out := make([]float32, int(d*time.Duration(rate)/time.Second))
	var b0, b1, b2, b3, b4, b5, b6 float64
	var sumSq float64
	raw := make([]float64, len(out))
	for i := range raw {
		w := rng.Float64()*2 - 1
		b0 = 0.99886*b0 + w*0.0555179
		b1 = 0.99332*b1 + w*0.0750759
		b2 = 0.96900*b2 + w*0.1538520
		b3 = 0.86650*b3 + w*0.3104856
		b4 = 0.55000*b4 + w*0.5329522
		b5 = -0.7616*b5 - w*0.0168980
		raw[i] = b0 + b1 + b2 + b3 + b4 + b5 + b6 + w*0.5362
		b6 = w * 0.115926
		sumSq += raw[i] * raw[i]
	}
	scale := rms / math.Sqrt(sumSq/float64(len(raw)))
	for i, v := range raw {
		out[i] = float32(v * scale)
	}
	return out
}

// decodeBoth returns the transcript of samples from Recognize and, for a
// streaming model, from a stream, which is the path the VAD masks.
func decodeBoth(t *testing.T, e *Engine, info engine.ModelInfo, samples []float32, rate int) map[string]string {
	t.Helper()
	got := map[string]string{}
	r, err := e.Recognize(context.Background(), samples, rate, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	got["recognize"] = r.Text
	if info.Streaming {
		run := streamClip(t, e, samples, rate, 20*time.Millisecond, 0, engine.Options{InterimResults: true})
		got["stream"] = run.final.Text
	}
	return got
}

// Long leading and trailing silence must not change the transcript: the
// padded clip reads the same words as the clip alone.
func TestVADPaddedSpeechKeepsTranscript(t *testing.T) {
	samples, rate, want := fixture(t)
	padded := padSilence(samples, rate, 4*time.Second)
	for _, file := range speechModels {
		t.Run(file, func(t *testing.T) {
			e, info := loadWithVAD(t, file)
			plain := decodeBoth(t, e, info, samples, rate)
			pad := decodeBoth(t, e, info, padded, rate)
			for path, text := range pad {
				if w := wer(want, text); w > maxWER {
					t.Errorf("%s: padded WER %.2f: %q", path, w, text)
				}
				if w := wer(plain[path], text); w > 0 {
					t.Errorf("%s: padding changed the transcript\n  plain:  %q\n  padded: %q", path, plain[path], text)
				}
			}
		})
	}
}

// Five seconds of quiet-room noise transcribe to nothing, so nothing would
// be pasted. The level, -45 dBFS RMS, is above the app's default silence
// threshold, so the clip reaches the model rather than being dropped before
// it.
func TestVADRoomNoiseTranscribesToNothing(t *testing.T) {
	const rate = 16000
	noise := pinkNoise(rate, 5*time.Second, math.Pow(10, -45.0/20), 1)
	for _, file := range speechModels {
		t.Run(file, func(t *testing.T) {
			e, info := loadWithVAD(t, file)
			for path, text := range decodeBoth(t, e, info, noise, rate) {
				if strings.TrimSpace(text) != "" {
					t.Errorf("%s: noise transcribed as %q", path, text)
				}
			}
		})
	}
}
