//go:build cgo && nemo && !race

package nemo

// raceEnabled reports a -race build, whose timings say nothing about real
// latency.
const raceEnabled = false
