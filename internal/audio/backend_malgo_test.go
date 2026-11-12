//go:build cgo

package audio

import (
	"testing"
	"time"

	"github.com/gen2brain/malgo"
)

// nullBackend is miniaudio's ma_backend_null. malgo v0.11.27's Backend
// enumeration lacks ma_backend_custom, so its BackendNull is one short.
const nullBackend = malgo.BackendNull + 1

// miniaudio's null backend has a capture device that delivers silence in
// real time, so the malgo path runs without hardware or permissions.
func TestMalgoNullBackendRecords(t *testing.T) {
	be := &malgoBackend{backends: []malgo.Backend{nullBackend}}
	devs, err := be.devices(captureDevices)
	if err != nil {
		t.Skipf("no null backend: %v", err)
	}
	if len(devs) == 0 || devs[0].ID == "" || devs[0].Name == "" {
		t.Fatalf("devices %+v", devs)
	}
	if _, err := be.devices(playbackDevices); err != nil {
		t.Errorf("playback devices: %v", err)
	}

	r := newRecorder(be, Options{Device: devs[0].Name, CloseDelay: -1})
	defer r.Close()
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	frames := r.Frames()
	streamed := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for f := range frames {
			streamed += len(f)
		}
	}()
	time.Sleep(300 * time.Millisecond)
	got := r.Stop()
	<-done
	rate := r.SampleRate()
	if rate < 8000 || len(got) < rate/10 || streamed != len(got) || r.Dropped() != 0 {
		t.Errorf("%d samples at %d Hz in 300 ms, %d streamed, %d dropped", len(got), rate, streamed, r.Dropped())
	}
	if l := Measure(got); l.Peak != 0 {
		t.Errorf("the null device is not silent: %+v", l)
	}
}

func TestMalgoBadDeviceID(t *testing.T) {
	be := &malgoBackend{backends: []malgo.Backend{nullBackend}}
	if _, err := be.open("zz", func([]float32) {}, func() {}); err == nil {
		t.Error("opened a device with a malformed ID")
	}
}
