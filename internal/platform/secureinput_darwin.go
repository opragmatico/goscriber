//go:build darwin

package platform

/*
#cgo LDFLAGS: -framework Carbon
#include <Carbon/Carbon.h>

static int goscriberSecureInputEnabled(void) {
	return IsSecureEventInputEnabled() ? 1 : 0;
}
*/
import "C"

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// SecureInputEnabled reports whether some process has turned on macOS
// Secure Event Input, as password fields and Terminal's Secure Keyboard
// Entry do. While it is on, keyboard event taps hear no key presses, so
// global shortcuts registered through one do not fire.
func SecureInputEnabled() bool { return C.goscriberSecureInputEnabled() != 0 }

// SecureInputHolder names the process holding Secure Event Input, as far as
// the IORegistry says. Apple documents no reliable way to find it: the PID
// may be missing or point at a parent such as loginwindow. ok is false when
// nothing could be found.
func SecureInputHolder() (pid int, name string, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ioreg", "-l", "-w", "0", "-d", "1", "-k", "IOConsoleUsers").Output()
	if err != nil {
		return 0, "", false
	}
	pid, ok = parseSecureInputPID(string(out))
	if !ok {
		return 0, "", false
	}
	if comm, err := exec.CommandContext(ctx, "ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output(); err == nil {
		name = processName(string(comm))
	}
	return pid, name, true
}

// processName is the last path element of ps's comm column.
func processName(comm string) string {
	comm = strings.TrimSpace(comm)
	if i := strings.LastIndexByte(comm, '/'); i >= 0 {
		comm = comm[i+1:]
	}
	return comm
}
