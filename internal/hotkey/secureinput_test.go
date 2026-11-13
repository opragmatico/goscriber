package hotkey

import (
	"testing"
	"time"
)

func TestSecureInputWatch(t *testing.T) {
	var w SecureInputWatch
	t0 := time.Unix(1000, 0)
	at := func(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }
	for i, tc := range []struct {
		on   bool
		at   float64
		want SecureInputChange
	}{
		{false, 0, SecureInputUnchanged},
		{true, 1, SecureInputUnchanged},  // a password field gains focus
		{false, 2, SecureInputUnchanged}, // and loses it: nothing to say
		{true, 10, SecureInputUnchanged},
		{true, 12, SecureInputUnchanged},
		{true, 13, SecureInputBlocking}, // on for 3 s
		{true, 14, SecureInputUnchanged},
		{true, 60, SecureInputUnchanged}, // told once
		{false, 61, SecureInputCleared},
		{false, 62, SecureInputUnchanged},
	} {
		if got := w.Observe(tc.on, at(tc.at)); got != tc.want {
			t.Fatalf("step %d (on=%v at %vs): %d, want %d", i, tc.on, tc.at, got, tc.want)
		}
	}
}
