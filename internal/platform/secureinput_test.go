package platform

import "testing"

func TestParseSecureInputPID(t *testing.T) {
	const sample = `+-o Root  <class IORegistryEntry, id 0x100000100, retain 25>
    {
      "IOConsoleUsers" = ({"kCGSSessionOnConsoleKey"=Yes,"kCGSSessionSecureInputPID"=48211,"kCGSessionLoginDoneKey"=Yes,"kCGSSessionUserIDKey"=501})
    }`
	if pid, ok := parseSecureInputPID(sample); !ok || pid != 48211 {
		t.Errorf("pid %d, ok %v", pid, ok)
	}
	if _, ok := parseSecureInputPID(`"IOConsoleUsers" = ({"kCGSSessionUserIDKey"=501})`); ok {
		t.Error("found a PID where there is none")
	}
	if _, ok := parseSecureInputPID(`"kCGSSessionSecureInputPID"=0`); ok {
		t.Error("PID 0 is no process")
	}
}
