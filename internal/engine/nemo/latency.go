package nemo

import (
	"fmt"

	"github.com/opragmatico/goscriber/internal/engine"
)

// rightContexts maps the latency presets to the encoder right context of a
// cache-aware streaming model, in 80 ms encoder frames: how far the model
// looks ahead of each chunk it decodes. A chunk is 1 + right context frames,
// and a stream produces at most one interim result per chunk.
//
// NVIDIA trains Nemotron 3.5 for right contexts 0, 1, 3, 6 and 13, and its
// GGUF records 3 as the default. -1 keeps that default: despite upstream's
// "model max" wording, the library replaces -1 with the GGUF's value, not
// the largest one the model supports.
//
//   - low, 1 (160 ms chunks): the library's own low-latency default.
//   - balanced, -1: the model's trained default (320 ms for Nemotron 3.5).
//   - accurate, 13 (1.12 s chunks): the setting of the model card's
//     headline word error rates.
//
// On the JFK fixture, Finish to final takes 18-44 ms on an M4 Max with
// Metal across these, and every one transcribes it without error; what
// changes is how often interim text updates (63, 31 and 9 times over 11 s).
var rightContexts = map[engine.Latency]int{
	engine.LatencyLow:      1,
	engine.LatencyBalanced: -1,
	engine.LatencyAccurate: 13,
}

// rightContext returns the encoder right context for latency l; empty means
// engine.LatencyBalanced.
func rightContext(op string, l engine.Latency) (int, error) {
	if l == "" {
		l = engine.LatencyBalanced
	}
	rc, ok := rightContexts[l]
	if !ok {
		return 0, &engine.Error{Op: op, Kind: engine.ErrInvalidArgument, Detail: fmt.Sprintf("unknown latency %q", l)}
	}
	return rc, nil
}
