package pipeline

import (
	"slices"
	"testing"
	"time"
)

// speech is d of a loud tone, standing in for speech, over quiet noise.
func speech(d time.Duration) []float32 { return mix(tone(d, 0.1), noise(d, -70)) }

// pause is d of quiet noise.
func pause(d time.Duration) []float32 { return noise(d, -70) }

func mix(a, b []float32) []float32 {
	out := slices.Clone(a)
	for i := range min(len(a), len(b)) {
		out[i] += b[i]
	}
	return out
}

func concat(parts ...[]float32) []float32 {
	var out []float32
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func at(d time.Duration) int { return int(d * rate / time.Second) }

// cutAll feeds samples to a chunker in pieces of step samples and returns
// where it cut, checking that the chunks and the rest add up to the input.
func cutAll(t *testing.T, samples []float32, step int, plan chunkPlan) []int {
	t.Helper()
	c := newChunker(rate, plan)
	var cuts []int
	var joined []float32
	for off := 0; off < len(samples); off += step {
		for _, ch := range c.add(samples[off:min(off+step, len(samples))]) {
			if ch.start != len(joined) {
				t.Fatalf("chunk starts at %d, want %d", ch.start, len(joined))
			}
			joined = append(joined, ch.samples...)
			cuts = append(cuts, len(joined))
		}
	}
	rest := c.rest()
	if rest.start != len(joined) {
		t.Fatalf("the rest starts at %d, want %d", rest.start, len(joined))
	}
	joined = append(joined, rest.samples...)
	if !slices.Equal(joined, samples) {
		t.Fatalf("the chunks add up to %d samples, not the %d pushed", len(joined), len(samples))
	}
	return cuts
}

func wantCutIn(t *testing.T, cut int, from, to time.Duration) {
	t.Helper()
	if cut < at(from) || cut > at(to) {
		t.Errorf("cut at %v, want it between %v and %v", time.Duration(cut)*time.Second/rate, from, to)
	}
}

func TestChunkerCutsInTheMiddleOfPauses(t *testing.T) {
	// 4 s of speech then a long pause, 5 s and a long pause, then 2 s.
	samples := concat(speech(4*time.Second), pause(time.Second), speech(5*time.Second), pause(time.Second), speech(2*time.Second))
	for _, step := range []int{1, 160, 441, at(time.Second)} {
		cuts := cutAll(t, samples, step, defaultChunkPlan)
		if len(cuts) != 2 {
			t.Fatalf("step %d: cuts %v, want one in each pause", step, cuts)
		}
		// A 4 s chunk needs about 660 ms of pause; the cut is in its middle.
		wantCutIn(t, cuts[0], 4*time.Second+300*time.Millisecond, 4*time.Second+400*time.Millisecond)
		wantCutIn(t, cuts[1], 10*time.Second+200*time.Millisecond, 10*time.Second+400*time.Millisecond)
	}
}

func TestChunkerShortChunksNeedLongPauses(t *testing.T) {
	// A 400 ms pause after 4 s is not enough; 800 ms after 9 s is.
	samples := concat(speech(4*time.Second), pause(400*time.Millisecond), speech(5*time.Second), pause(800*time.Millisecond), speech(time.Second))
	cuts := cutAll(t, samples, 160, defaultChunkPlan)
	if len(cuts) != 1 {
		t.Fatalf("cuts %v, want one, in the second pause", cuts)
	}
	wantCutIn(t, cuts[0], 9400*time.Millisecond, 10*time.Second)

	// Past 15 s a 250 ms pause ends a chunk; nothing shorter does.
	samples = concat(speech(16*time.Second), pause(200*time.Millisecond), speech(time.Second), pause(300*time.Millisecond), speech(time.Second))
	cuts = cutAll(t, samples, 160, defaultChunkPlan)
	if len(cuts) != 1 {
		t.Fatalf("cuts %v, want one, in the 300 ms pause", cuts)
	}
	wantCutIn(t, cuts[0], 17200*time.Millisecond, 17500*time.Millisecond)

	// No chunk is shorter than 3 s: a pause from 2 s to 4 s ends the chunk
	// at 3 s, not in its middle.
	samples = concat(speech(2*time.Second), pause(2*time.Second), speech(2*time.Second))
	if cuts := cutAll(t, samples, 160, defaultChunkPlan); !slices.Equal(cuts, []int{at(3 * time.Second)}) {
		t.Errorf("cuts %v in 6 s with a pause from 2 to 4 s, want one at 3 s", cuts)
	}
}

func TestChunkerForcesACutAtTheQuietestMoment(t *testing.T) {
	// 30 s of unbroken speech with a 100 ms dip at 21 s: the 25 s chunk
	// ends in the dip.
	samples := concat(speech(21*time.Second), pause(100*time.Millisecond), speech(9*time.Second))
	cuts := cutAll(t, samples, 160, defaultChunkPlan)
	if len(cuts) != 1 {
		t.Fatalf("cuts %v, want one", cuts)
	}
	wantCutIn(t, cuts[0], 21*time.Second, 21100*time.Millisecond)

	// With no dip at all it still ends by 25 s, and no chunk is longer.
	cuts = cutAll(t, speech(60*time.Second), 160, defaultChunkPlan)
	prev := 0
	for _, c := range cuts {
		if l := time.Duration(c-prev) * time.Second / rate; l < defaultChunkPlan.minChunk || l > defaultChunkPlan.maxChunk {
			t.Errorf("a chunk of %v", l)
		}
		prev = c
	}
	if len(samples)-prev > at(defaultChunkPlan.maxChunk) || len(cuts) < 2 {
		t.Errorf("cuts %v in 60 s of speech", cuts)
	}
}

func TestChunkerSilenceAndNoise(t *testing.T) {
	// Long silence, digital or not, is cut into chunks of at least 3 s.
	for name, quiet := range map[string][]float32{"digital": make([]float32, at(10*time.Second)), "noise": pause(10 * time.Second)} {
		samples := concat(speech(4*time.Second), quiet, speech(time.Second))
		cuts := cutAll(t, samples, 160, defaultChunkPlan)
		if len(cuts) == 0 {
			t.Errorf("%s: no cut in 10 s of silence", name)
		}
		prev := 0
		for _, c := range cuts {
			if c-prev < at(defaultChunkPlan.minChunk) {
				t.Errorf("%s: a chunk of %v", name, time.Duration(c-prev)*time.Second/rate)
			}
			prev = c
		}
	}

	// The pause threshold follows the noise floor: over noise at -40 dBFS,
	// louder speech still has its pauses found.
	loud := func(d time.Duration) []float32 { return mix(tone(d, 0.3), noise(d, -40)) }
	samples := concat(loud(5*time.Second), noise(time.Second, -40), loud(5*time.Second))
	cuts := cutAll(t, samples, 160, defaultChunkPlan)
	if len(cuts) != 1 {
		t.Fatalf("noisy room: cuts %v, want one in the pause", cuts)
	}
	wantCutIn(t, cuts[0], 5*time.Second, 6*time.Second)
}

func TestNeededPause(t *testing.T) {
	c := newChunker(rate, defaultChunkPlan)
	for length, want := range map[time.Duration]time.Duration{
		3 * time.Second:  700 * time.Millisecond,
		9 * time.Second:  475 * time.Millisecond,
		15 * time.Second: 250 * time.Millisecond,
		24 * time.Second: 250 * time.Millisecond,
	} {
		if got := c.neededPause(length); got != want {
			t.Errorf("neededPause(%v) = %v, want %v", length, got, want)
		}
	}
}
