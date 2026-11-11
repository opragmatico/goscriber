//go:build cgo && nemo

// Command nemo-version prints the version of the NeMo-Speech.cpp runtime the
// CGo binding loads, such as 0.3.0. It checks a `make deps` install end to
// end: pkg-config finds the SDK, the linker finds its libraries, and the
// program finds them again at run time.
//
//	make engine-version
package main

import (
	"fmt"

	"github.com/opragmatico/goscriber/internal/engine/nemo"
)

func main() {
	fmt.Println(nemo.Version())
}
