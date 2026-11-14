package pipeline

import (
	"math"
	"slices"
	"time"
)

// chunkPlan says where a recording may be cut into chunks for an offline
// model. Chunks end in the middle of a pause, so no word is split and none
// is decoded twice. A short chunk needs a long pause, which is most likely
// the end of a sentence; the longer a chunk grows, the shorter the pause
// that ends it. A chunk that reaches maxChunk without a pause ends at the
// quietest moment of its last search window instead.
type chunkPlan struct {
	window     time.Duration // the loudness of the audio is measured per window
	minChunk   time.Duration // no chunk is shorter, but the last
	fullChunk  time.Duration // from here on a short pause ends a chunk
	maxChunk   time.Duration // no chunk is longer
	longPause  time.Duration // the pause that ends a chunk of minChunk
	shortPause time.Duration // the pause that ends a chunk of fullChunk or more
	search     time.Duration // where a chunk of maxChunk looks for its quietest moment
	quietest   time.Duration // the stretch of audio that moment is measured over
	history    time.Duration // the audio the pause threshold follows
}

// defaultChunkPlan keeps chunks between 3 and 25 s. Parakeet TDT decodes
// 25 s in about 140 ms on an M4 Max with Metal, so the chunk still open
// when the user stops is decoded well within the release budget, and no
// chunk comes near the 90 s inputs that came back empty in the model's
// checks (docs/models/parakeet-tdt-0.6b-v2.md#long-audio).
var defaultChunkPlan = chunkPlan{
	window:     20 * time.Millisecond,
	minChunk:   3 * time.Second,
	fullChunk:  15 * time.Second,
	maxChunk:   25 * time.Second,
	longPause:  700 * time.Millisecond,
	shortPause: 250 * time.Millisecond,
	search:     10 * time.Second,
	quietest:   100 * time.Millisecond,
	history:    10 * time.Second,
}

// Loudness the pause detector works with, in dBFS.
const (
	// quietFloorDB: anything quieter is a pause whatever else the audio
	// holds; the default silence_threshold_db.
	quietFloorDB = -60
	// silentDB stands in for digital silence, whose level is -Inf.
	silentDB = -120
	// quietShare places the pause threshold between the noise floor and
	// the speech level, as a share of the decibels between them.
	quietShare = 0.35
)

// chunk is a piece of a recording.
type chunk struct {
	start   int // offset of its first sample in the recording
	samples []float32
}

// chunker cuts a recording into chunks at pauses as it arrives. Pauses are
// found by loudness, not by the VAD, because NeMo-Speech.cpp only runs its
// VAD inside the recognizer: a window is quiet when its level is below a
// threshold that follows the recording's noise floor and speech level.
type chunker struct {
	plan   chunkPlan
	window int // samples per window

	start    int       // offset of the open chunk in the recording
	buf      []float32 // the open chunk's samples
	levels   []float64 // the level of each whole window of the open chunk, in dBFS
	quietRun int       // quiet windows at the end of the open chunk

	recent    []float64 // the latest levels, a ring of history/window entries
	recentAt  int       // where the next level goes in recent
	threshold float64   // a window below this level is quiet
	sinceCalc int       // windows since threshold was worked out
	scratch   []float64
}

// newChunker returns a chunker for audio at rate Hz.
func newChunker(rate int, plan chunkPlan) *chunker {
	window := max(1, int(int64(rate)*int64(plan.window)/int64(time.Second)))
	return &chunker{
		plan:      plan,
		window:    window,
		recent:    make([]float64, 0, max(1, int(plan.history/plan.window))),
		threshold: quietFloorDB,
	}
}

// add takes the next samples of the recording and returns the chunks they
// complete, in order. The chunks own their samples.
func (c *chunker) add(p []float32) []chunk {
	var out []chunk
	for len(p) > 0 {
		// Feed up to the end of the current window, then judge it.
		need := c.window - len(c.buf)%c.window
		n := min(need, len(p))
		c.buf = append(c.buf, p[:n]...)
		p = p[n:]
		if n < need {
			break
		}
		c.measure(c.buf[len(c.buf)-c.window:])
		if cut, ok := c.cutPoint(); ok {
			out = append(out, c.cut(cut))
		}
	}
	return out
}

// rest returns the open chunk: what follows the last cut.
func (c *chunker) rest() chunk { return chunk{start: c.start, samples: c.buf} }

// measure records the level of one whole window.
func (c *chunker) measure(w []float32) {
	var sum float64
	for _, s := range w {
		sum += float64(s) * float64(s)
	}
	db := float64(silentDB)
	if sum > 0 {
		db = max(10*math.Log10(sum/float64(len(w))), silentDB)
	}
	c.levels = append(c.levels, db)

	if len(c.recent) < cap(c.recent) {
		c.recent = append(c.recent, db)
	} else {
		c.recent[c.recentAt] = db
	}
	c.recentAt = (c.recentAt + 1) % cap(c.recent)
	if c.sinceCalc++; c.sinceCalc >= 5 || len(c.recent) <= 5 {
		c.threshold = c.quietThreshold()
		c.sinceCalc = 0
	}

	if db < c.threshold {
		c.quietRun++
	} else {
		c.quietRun = 0
	}
}

// quietThreshold places the pause threshold between the noise floor (the
// 10th percentile of the recent levels) and the speech level (the 90th),
// and never under the quiet floor.
func (c *chunker) quietThreshold() float64 {
	c.scratch = append(c.scratch[:0], c.recent...)
	slices.Sort(c.scratch)
	floor := c.scratch[len(c.scratch)/10]
	speech := c.scratch[len(c.scratch)*9/10]
	return max(floor+quietShare*(speech-floor), quietFloorDB)
}

// cutPoint says whether the open chunk should end now, and where: an offset
// in its windows.
func (c *chunker) cutPoint() (int, bool) {
	windows := len(c.levels)
	length := time.Duration(windows) * c.plan.window
	if length < c.plan.minChunk {
		return 0, false
	}
	// Only the part of the pause inside this chunk counts: after a cut in
	// a long silence the rest of it opens the next chunk.
	pause := min(c.quietRun, windows)
	if pause > 0 && time.Duration(pause)*c.plan.window >= c.neededPause(length) {
		return max(windows-pause/2, int(c.plan.minChunk/c.plan.window)), true
	}
	if length < c.plan.maxChunk {
		return 0, false
	}
	return c.quietestPoint(), true
}

// neededPause is how long a pause must be to end a chunk of the given
// length: longPause at minChunk, falling to shortPause at fullChunk.
func (c *chunker) neededPause(length time.Duration) time.Duration {
	p := c.plan
	if length >= p.fullChunk {
		return p.shortPause
	}
	frac := float64(length-p.minChunk) / float64(p.fullChunk-p.minChunk)
	return p.longPause - time.Duration(frac*float64(p.longPause-p.shortPause))
}

// quietestPoint is the middle of the quietest stretch of the open chunk's
// search window, which must leave at least minChunk before it.
func (c *chunker) quietestPoint() int {
	windows := len(c.levels)
	span := max(1, int(c.plan.quietest/c.plan.window))
	lo := max(int(c.plan.minChunk/c.plan.window), windows-int(c.plan.search/c.plan.window))
	best, bestAt := math.Inf(1), windows-span
	for i := lo; i+span <= windows; i++ {
		var power float64
		for _, db := range c.levels[i : i+span] {
			power += math.Pow(10, db/10)
		}
		if power < best {
			best, bestAt = power, i
		}
	}
	return bestAt + span/2
}

// cut ends the open chunk after the given number of windows, opens the next
// with what follows, and returns the chunk it ended.
func (c *chunker) cut(windows int) chunk {
	n := windows * c.window
	done := chunk{start: c.start, samples: c.buf[:n:n]}
	c.buf = append([]float32(nil), c.buf[n:]...)
	c.levels = append([]float64(nil), c.levels[windows:]...)
	c.quietRun = min(c.quietRun, len(c.levels))
	c.start += n
	return done
}
