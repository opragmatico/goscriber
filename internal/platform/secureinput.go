package platform

import (
	"regexp"
	"strconv"
)

var secureInputPIDRe = regexp.MustCompile(`"kCGSSessionSecureInputPID"\s*=\s*(\d+)`)

// parseSecureInputPID finds the PID of the Secure Event Input holder in
// `ioreg -l` output, where the console session records it.
func parseSecureInputPID(ioreg string) (int, bool) {
	m := secureInputPIDRe.FindStringSubmatch(ioreg)
	if m == nil {
		return 0, false
	}
	pid, err := strconv.Atoi(m[1])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
