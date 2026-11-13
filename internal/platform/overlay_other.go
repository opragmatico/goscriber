//go:build !darwin

package platform

// PrepareOverlay adjusts the recording overlay's native window on macOS;
// elsewhere Fyne's always-on-top request is all there is.
func PrepareOverlay(any, float32) {}

// PlaceOverlay positions the recording overlay on macOS; elsewhere the
// window stays centred on the screen.
func PlaceOverlay(any, bool, float32) {}

// OverlayClickThrough lets clicks through the overlay on macOS; elsewhere
// the whole overlay takes clicks.
func OverlayClickThrough(any, float32, float32, float32, float32) {}

// YieldFocus hands the keyboard back to the previous app on macOS;
// elsewhere it does nothing.
func YieldFocus() {}
