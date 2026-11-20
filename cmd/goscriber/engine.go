//go:build cgo && nemo

package main

import (
	"github.com/opragmatico/goscriber/engine"
	"github.com/opragmatico/goscriber/engine/nemo"
)

// The NeMo-Speech.cpp engine needs the SDK from `make deps` and the nemo
// build tag, which `make build` adds when the SDK is there.
func init() {
	newEngine = func() engine.Engine { return nemo.New() }
	engineVersion = nemo.Version
	engineGPU = nemo.GPUBackend
}
