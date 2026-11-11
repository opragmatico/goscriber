package nemo

import (
	"errors"
	"testing"

	"github.com/opragmatico/goscriber/internal/engine"
)

func TestRightContext(t *testing.T) {
	for l, want := range map[engine.Latency]int{
		"":                     -1,
		engine.LatencyLow:      1,
		engine.LatencyBalanced: -1,
		engine.LatencyAccurate: 13,
	} {
		if got, err := rightContext("load", l); err != nil || got != want {
			t.Errorf("rightContext(%q) = %d, %v; want %d", l, got, err, want)
		}
	}
	if _, err := rightContext("load", "instant"); !errors.Is(err, engine.ErrInvalidArgument) {
		t.Errorf("unknown latency: err = %v, want ErrInvalidArgument", err)
	}
}
