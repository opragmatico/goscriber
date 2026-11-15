package app

import (
	"testing"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/fake"
	"github.com/opragmatico/goscriber/internal/engine/worker"
	"github.com/opragmatico/goscriber/internal/notice"
)

// The engine runs in a worker process unless engine_in_process or
// --engine-in-process keeps it in the app.
func TestSpeechEngine(t *testing.T) {
	isolate(t)
	rt := start(t, Options{})
	hub := notice.NewHub(notice.Options{})
	inApp := fake.New(fake.Script{})
	newEngine := func() engine.Engine { return inApp }
	def := config.Defaults()
	inProcess := def
	inProcess.EngineInProcess = true

	if _, ok := speechEngine(rt, Options{}, def, hub).(noEngine); !ok {
		t.Error("a build without an engine got one")
	}
	if _, ok := speechEngine(rt, Options{NewEngine: newEngine}, def, hub).(*worker.Supervisor); !ok {
		t.Error("the engine does not run in a worker by default")
	}
	if e := speechEngine(rt, Options{NewEngine: newEngine}, inProcess, hub); e != inApp {
		t.Errorf("engine_in_process: got %T, want the engine itself", e)
	}
	if e := speechEngine(rt, Options{NewEngine: newEngine, EngineInProcess: true}, def, hub); e != inApp {
		t.Errorf("--engine-in-process: got %T, want the engine itself", e)
	}
}
