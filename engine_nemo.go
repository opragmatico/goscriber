//go:build cgo && nemo

package goscriber

import (
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/nemo"
)

// A program built with the nemo tag, against the NeMo-Speech.cpp SDK that
// make deps builds, carries the engine, so Options.InProcess can run it.
func init() {
	inProcessEngine = func() engine.Engine { return nemo.New() }
}
