package audio

import "math"

// Levels never reach the speech model, which takes the raw samples and copes
// with a wide range of loudness. They exist for two reasons:
//
//   - Feedback: the overlay's meter shows the user that the right microphone
//     is live while they speak, which push-to-talk otherwise gives no sign of.
//   - Catching bad input before it becomes a bad transcript. A peak of exactly
//     0 almost always means no microphone permission or the wrong device:
//     macOS delivers zeros rather than an error, which would otherwise end as
//     an unexplained empty transcript. Very quiet input (a distant or muted
//     microphone) costs accuracy, and peaks pinned at full scale (clipping)
//     mean distorted audio, which does too.

// LevelRate is how many Levels a recording reports per second, enough for a
// smooth meter in the overlay.
const LevelRate = 30

// Level is how loud the microphone was over the last 1/LevelRate seconds.
type Level struct {
	// RMS is the root mean square of the samples, from 0 to 1.
	RMS float32
	// Peak is the largest absolute sample, from 0 to 1 (more if the device
	// delivers samples past full scale).
	Peak float32
}

// DBFS returns v in decibels relative to full scale, floored at -100 dB.
func DBFS(v float32) float64 {
	if v <= 1e-5 {
		return -100
	}
	return 20 * math.Log10(float64(v))
}

// meter turns samples into Levels, one per window of rate/LevelRate samples.
type meter struct {
	window int
	n      int
	sumSq  float64
	peak   float32
}

func newMeter(rate int) *meter { return &meter{window: max(1, rate/LevelRate)} }

// add feeds samples and calls emit for each window they complete.
func (m *meter) add(p []float32, emit func(Level)) {
	for _, s := range p {
		m.sumSq += float64(s) * float64(s)
		if a := float32(math.Abs(float64(s))); a > m.peak {
			m.peak = a
		}
		m.n++
		if m.n == m.window {
			emit(Level{RMS: float32(math.Sqrt(m.sumSq / float64(m.n))), Peak: m.peak})
			m.n, m.sumSq, m.peak = 0, 0, 0
		}
	}
}

// reset drops a partial window.
func (m *meter) reset() { m.n, m.sumSq, m.peak = 0, 0, 0 }

// Measure returns the Level of a whole buffer.
func Measure(p []float32) Level {
	if len(p) == 0 {
		return Level{}
	}
	var lv Level
	m := &meter{window: len(p)}
	m.add(p, func(l Level) { lv = l })
	return lv
}

// Loudest returns the Level of the loudest 1/LevelRate window of p at rate
// Hz: the RMS of that window and the largest absolute sample of the whole
// buffer. A buffer shorter than one window is measured whole. Unlike the
// RMS of the whole buffer, it does not fall as silence around a short word
// grows, so it tells a near-silent recording from a quiet one with speech.
func Loudest(p []float32, rate int) Level {
	m := newMeter(rate)
	if len(p) < m.window {
		return Measure(p)
	}
	var lv Level
	m.add(p, func(l Level) {
		lv.RMS = max(lv.RMS, l.RMS)
		lv.Peak = max(lv.Peak, l.Peak)
	})
	if m.n > 0 { // the partial last window still counts for the peak
		lv.Peak = max(lv.Peak, m.peak)
	}
	return lv
}
