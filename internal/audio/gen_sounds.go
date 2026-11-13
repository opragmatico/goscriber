//go:build ignore

// gen_sounds writes the start and stop cues, sounds/start.wav and
// sounds/stop.wav: two short sine chimes, rising to start and falling to
// stop, made here from scratch so they carry no third-party licence. Run it
// with `go generate ./internal/audio` after changing them.
package main

import (
	"log"
	"math"

	"github.com/opragmatico/goscriber/internal/audio"
)

const rate = 24000 // the cues have nothing above 4 kHz

// chime is one soft note: a 4 ms attack, then an exponential decay, with a
// little of the second and third harmonics for warmth.
type chime struct {
	freq         float64
	at, length   float64 // seconds
	decay, level float64
}

func render(notes ...chime) []float32 {
	end := 0.0
	for _, n := range notes {
		end = math.Max(end, n.at+n.length)
	}
	out := make([]float64, int(end*rate))
	for _, n := range notes {
		start := int(n.at * rate)
		for i := 0; i < int(n.length*rate) && start+i < len(out); i++ {
			t := float64(i) / rate
			env := math.Min(1, t/0.004) * math.Exp(-t/n.decay)
			// Fade the tail of each note to nothing, so it ends without a click.
			if left := n.length - t; left < 0.012 {
				env *= left / 0.012
			}
			w := 2 * math.Pi * n.freq * t
			out[start+i] += n.level * env * (math.Sin(w) + 0.22*math.Sin(2*w) + 0.08*math.Sin(3*w))
		}
	}
	peak := 0.0
	for _, v := range out {
		peak = math.Max(peak, math.Abs(v))
	}
	samples := make([]float32, len(out))
	for i, v := range out {
		samples[i] = float32(v / peak * 0.5) // -6 dBFS
	}
	return samples
}

func main() {
	const a5, e6 = 880.0, 1318.51
	start := render(
		chime{freq: a5, at: 0, length: 0.11, decay: 0.045, level: 0.8},
		chime{freq: e6, at: 0.06, length: 0.16, decay: 0.06, level: 1},
	)
	stop := render(
		chime{freq: e6, at: 0, length: 0.11, decay: 0.045, level: 0.8},
		chime{freq: a5, at: 0.06, length: 0.16, decay: 0.06, level: 1},
	)
	for name, s := range map[string][]float32{"sounds/start.wav": start, "sounds/stop.wav": stop} {
		if err := audio.WriteWAVFile(name, s, rate); err != nil {
			log.Fatal(err)
		}
	}
}
