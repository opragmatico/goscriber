package audio

import (
	"math"
	"slices"
	"sync"
	"testing"
)

func ramp(from, n int) []float32 {
	p := make([]float32, n)
	for i := range p {
		p[i] = float32(from + i)
	}
	return p
}

func TestRingWrapsAround(t *testing.T) {
	q := newRing(10) // rounds up to 16
	if len(q.buf) != 16 {
		t.Fatalf("capacity %d", len(q.buf))
	}
	dst := make([]float32, 16)
	next := 0
	for round := range 20 {
		n := 1 + round%7
		q.write(ramp(next, n))
		got := q.read(dst)
		if !slices.Equal(dst[:got], ramp(next, n)) {
			t.Fatalf("round %d: read %v", round, dst[:got])
		}
		next += n
	}
	if q.dropped.Load() != 0 || q.buffered() != 0 {
		t.Errorf("dropped %d, buffered %d", q.dropped.Load(), q.buffered())
	}
}

func TestRingDropsWhenFull(t *testing.T) {
	q := newRing(8)
	q.write(ramp(0, 5))
	q.write(ramp(5, 5)) // only 3 fit
	if q.dropped.Load() != 2 || q.buffered() != 8 {
		t.Fatalf("dropped %d, buffered %d", q.dropped.Load(), q.buffered())
	}
	dst := make([]float32, 3)
	if n := q.read(dst); n != 3 || !slices.Equal(dst, ramp(0, 3)) {
		t.Errorf("partial read %d %v", n, dst)
	}
	all := make([]float32, 100)
	n := q.read(all)
	if !slices.Equal(all[:n], ramp(3, 5)) {
		t.Errorf("rest %v", all[:n])
	}
	if q.read(all) != 0 {
		t.Error("read from an empty ring")
	}
}

// One writer and one reader at once, as the device callback and the pump:
// every sample arrives once, in order. Run with -race.
func TestRingConcurrent(t *testing.T) {
	q := newRing(256)
	const total = 200_000
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for sent := 0; sent < total; {
			n := min(97, total-sent)
			if q.buffered()+n > len(q.buf) {
				continue // wait for room rather than drop
			}
			q.write(ramp(sent, n))
			sent += n
		}
	}()
	dst := make([]float32, 64)
	for want := 0; want < total; {
		n := q.read(dst)
		for _, v := range dst[:n] {
			if v != float32(want) {
				t.Fatalf("got %v, want %d", v, want)
			}
			want++
		}
	}
	wg.Wait()
	if q.dropped.Load() != 0 {
		t.Errorf("dropped %d", q.dropped.Load())
	}
}

func TestHistoryKeepsTheLatest(t *testing.T) {
	h := newHistory(5)
	if got := h.take(); len(got) != 0 {
		t.Errorf("empty history gave %v", got)
	}
	h.add(ramp(0, 3))
	if got := h.take(); !slices.Equal(got, ramp(0, 3)) {
		t.Errorf("partial: %v", got)
	}
	h.add(ramp(0, 3))
	h.add(ramp(3, 4)) // wraps
	if got := h.take(); !slices.Equal(got, ramp(2, 5)) {
		t.Errorf("wrapped: %v", got)
	}
	h.add(ramp(0, 12)) // more than fits at once
	if got := h.take(); !slices.Equal(got, ramp(7, 5)) {
		t.Errorf("overfull: %v", got)
	}
	for i := range 5 {
		h.add(ramp(i, 1)) // exactly full
	}
	if got := h.take(); !slices.Equal(got, ramp(0, 5)) {
		t.Errorf("exactly full: %v", got)
	}
	newHistory(0).add(ramp(0, 3)) // no pre-roll: no panic
}

func sine(rate, n int, amp, hz float64) []float32 {
	p := make([]float32, n)
	for i := range p {
		p[i] = float32(amp * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
	}
	return p
}

func TestMeterWindows(t *testing.T) {
	m := newMeter(48000) // 1600-sample windows
	var levels []Level
	emit := func(l Level) { levels = append(levels, l) }
	m.add(sine(48000, 1000, 0.5, 1000), emit)
	if len(levels) != 0 {
		t.Fatalf("emitted before a full window: %v", levels)
	}
	m.add(sine(48000, 4000, 0.5, 1000), emit) // 5000 samples: 3 windows
	if len(levels) != 3 {
		t.Fatalf("%d levels from 5000 samples", len(levels))
	}
	for _, l := range levels {
		// A sine's RMS is its amplitude over √2.
		if math.Abs(float64(l.RMS)-0.5/math.Sqrt2) > 0.01 || math.Abs(float64(l.Peak)-0.5) > 0.01 {
			t.Errorf("level %+v, want RMS 0.354 and peak 0.5", l)
		}
	}
	m.reset()
	m.add(make([]float32, 1600), emit)
	if l := levels[len(levels)-1]; l.RMS != 0 || l.Peak != 0 {
		t.Errorf("silence after reset: %+v", l)
	}
}

func TestMeasureAndDBFS(t *testing.T) {
	l := Measure([]float32{0.5, -0.5, 0.5, -1})
	if math.Abs(float64(l.RMS)-math.Sqrt(1.75/4)) > 1e-6 || l.Peak != 1 {
		t.Errorf("Measure %+v", l)
	}
	if (Measure(nil) != Level{}) {
		t.Error("Measure(nil)")
	}
	for v, want := range map[float32]float64{1: 0, 0.5: -6.02, 0: -100, 0.1: -20} {
		if got := DBFS(v); math.Abs(got-want) > 0.01 {
			t.Errorf("DBFS(%v) = %.2f, want %.2f", v, got, want)
		}
	}
}
