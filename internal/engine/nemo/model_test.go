package nemo

import (
	"errors"
	"testing"

	"github.com/opragmatico/goscriber/internal/engine"
)

func TestCheckVAD(t *testing.T) {
	for _, tc := range []struct {
		onset, offset float32
		ok            bool
	}{
		{0, 0, true},       // the library's 0.5 and 0.3
		{0.5, 0.3, true},   // the same, explicitly
		{0.6, 0.6, true},   // offset may equal onset
		{0.2, 0, false},    // the default offset 0.3 is above 0.2
		{0, 0.6, false},    // above the default onset 0.5
		{0.4, 0.5, false},  // offset above onset
		{1, 0.3, false},    // out of range
		{0.5, -0.1, false}, // out of range
	} {
		err := checkVAD("load", engine.LoadSpec{VADOnset: tc.onset, VADOffset: tc.offset})
		if (err == nil) != tc.ok {
			t.Errorf("onset %g offset %g: err = %v, want ok %v", tc.onset, tc.offset, err, tc.ok)
		}
		if err != nil && !errors.Is(err, engine.ErrInvalidArgument) {
			t.Errorf("onset %g offset %g: err = %v, want ErrInvalidArgument", tc.onset, tc.offset, err)
		}
	}
}
