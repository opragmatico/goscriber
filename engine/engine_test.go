package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
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

func TestJoinFinals(t *testing.T) {
	r := JoinFinals([]Result{
		{Text: "one.", Confidence: 1, Languages: []string{"en-US"}, AudioProcessed: time.Second, Words: []Word{{Text: "one."}}},
		{Text: " ", Confidence: 0},
		{Text: "two.", Confidence: 0.5, Languages: []string{"en-US", "de-DE"}, AudioProcessed: 2 * time.Second, Words: []Word{{Text: "two."}}},
	})
	if r.Text != "one. two." || !r.Final || len(r.Words) != 2 || r.AudioProcessed != 2*time.Second ||
		!slices.Equal(r.Languages, []string{"en-US", "de-DE"}) || r.Confidence != 0.5 {
		t.Errorf("joined %+v", r)
	}
	one := Result{Text: "alone", Final: true, Confidence: 0.9}
	if got := JoinFinals([]Result{one}); !reflect.DeepEqual(got, one) {
		t.Errorf("one final joined into %+v", got)
	}
	if got := JoinFinals(nil); got.Text != "" || !got.Final {
		t.Errorf("no finals joined into %+v", got)
	}
}

func TestResultOffset(t *testing.T) {
	r := Result{
		Text:           "a b",
		Words:          []Word{{Text: "a", Start: 0, End: time.Second}, {Text: "b", Start: time.Second, End: 2 * time.Second}},
		AudioProcessed: 2 * time.Second,
	}
	got := r.Offset(10 * time.Second)
	if got.Words[0].Start != 10*time.Second || got.Words[1].End != 12*time.Second || got.AudioProcessed != 12*time.Second {
		t.Errorf("offset %+v", got)
	}
	if r.Words[0].Start != 0 {
		t.Error("Offset changed the original's words")
	}
}
