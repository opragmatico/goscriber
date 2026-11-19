//go:build !unix

package hook

import "os/exec"

// stopGroup leaves cancelling cmd to os/exec, which kills its program alone:
// there is no process group to signal.
func stopGroup(*exec.Cmd) (kill func()) { return func() {} }
