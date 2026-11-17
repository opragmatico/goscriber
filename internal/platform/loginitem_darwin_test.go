//go:build darwin

package platform

import (
	"errors"
	"testing"
)

// The test binary is a bare executable, which macOS gives no login item:
// the functions say so, and none tries to register one.
func TestLoginItemNeedsTheAppBundle(t *testing.T) {
	err := LoginItemSupported()
	if !errors.Is(err, ErrLoginItemUnsupported) {
		t.Fatalf("LoginItemSupported() = %v, want ErrLoginItemUnsupported", err)
	}
	if on, err := LoginItemEnabled(); on || !errors.Is(err, ErrLoginItemUnsupported) {
		t.Errorf("LoginItemEnabled() = %v, %v", on, err)
	}
	for _, on := range []bool{true, false} {
		if err := SetLoginItem(on); !errors.Is(err, ErrLoginItemUnsupported) {
			t.Errorf("SetLoginItem(%v) = %v", on, err)
		}
	}
}

// Without AppKit finishing a launch there is no login item launch to see.
func TestLaunchedAtLoginWithoutALaunch(t *testing.T) {
	WatchLaunch()
	WatchLaunch()
	if LaunchedAtLogin() {
		t.Error("a test binary reads as launched at login")
	}
}
