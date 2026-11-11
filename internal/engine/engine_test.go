package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestDecodeMode(t *testing.T) {
	if got := (ModelInfo{Streaming: true}).DecodeMode(); got != DecodeLive {
		t.Errorf("streaming model: DecodeMode = %v, want %v", got, DecodeLive)
	}
	if got := (ModelInfo{}).DecodeMode(); got != DecodeOnce {
		t.Errorf("offline model: DecodeMode = %v, want %v", got, DecodeOnce)
	}
	for m, want := range map[DecodeMode]string{DecodeOnce: "once", DecodeLive: "live", DecodeMode(7): "unknown"} {
		if got := m.String(); got != want {
			t.Errorf("DecodeMode(%d).String() = %q, want %q", int(m), got, want)
		}
	}
}

func TestErrorMessage(t *testing.T) {
	for _, tc := range []struct {
		err  *Error
		want string
	}{
		{&Error{Op: "recognize", Kind: ErrInvalidArgument, Detail: "empty audio"}, "engine: recognize: invalid argument: empty audio"},
		{&Error{Op: "load", Kind: ErrRuntime}, "engine: load: runtime error"},
		{&Error{Op: "recognize", Kind: ErrCancelled, Err: context.Canceled}, "engine: recognize: cancelled: context canceled"},
		{&Error{Kind: ErrNotLoaded}, "engine: no model loaded"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
	}
}

func TestErrorIs(t *testing.T) {
	err := fmt.Errorf("transcribe: %w", &Error{Op: "recognize", Kind: ErrCancelled, Err: context.DeadlineExceeded})
	if !errors.Is(err, ErrCancelled) {
		t.Error("errors.Is(err, ErrCancelled) = false")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("errors.Is(err, context.DeadlineExceeded) = false")
	}
	if errors.Is(err, ErrRuntime) {
		t.Error("errors.Is(err, ErrRuntime) = true")
	}
	var e *Error
	if !errors.As(err, &e) || e.Op != "recognize" {
		t.Errorf("errors.As found %+v", e)
	}
	if errs := (&Error{Kind: ErrRuntime}).Unwrap(); len(errs) != 1 {
		t.Errorf("Unwrap without a cause = %v, want only the kind", errs)
	}
}
