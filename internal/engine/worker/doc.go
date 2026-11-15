// Package worker runs the native engine in a supervised child process, so a
// crash inside NeMo-Speech.cpp cannot take the app down.
//
// With CGo, a GGML_ASSERT, an abort() or a GPU driver fault in the engine
// ends the whole Go process. So the app runs the engine in a worker: the
// goscriber executable started again with the hidden engine-worker
// subcommand (Command), whose Run hosts the NeMo-Speech.cpp binding and
// serves requests on stdin and stdout. The app talks to it through a
// Supervisor, which implements engine.Engine, so the coordinator and the
// idle unloader (engine/autounload) do not know a process is involved.
//
// # Protocol
//
// Every message is a frame: a little-endian uint32 length and JSON, then a
// little-endian uint32 length and raw little-endian float32 audio (empty
// for most requests). Each request carries an ID and gets one response with
// the same ID; requests may overlap, and the worker answers pings even while
// it decodes. The requests mirror engine.Engine and engine.Stream: hello,
// ping, load, unload, recognize, and stream start, push, next, endpoint,
// finish and close.
//
// # Supervision
//
//   - A worker lives as long as its model: Load starts one, and Unload or the
//     next Load ends it, so unloading returns all the engine's memory to the
//     OS.
//   - A worker that exits, misses a request's timeout or a heartbeat ping is
//     lost. The supervisor publishes notice.EngineCrashed, and the next call
//     starts a new worker that loads the same model; the call that was
//     waiting is retried there once (not a decode that hung on the CPU,
//     which is slow rather than broken). A stream cannot be replayed: its
//     calls fail with ErrWorkerLost, and the caller decodes the recording
//     with Recognize instead.
//   - A worker lost while it loads or decodes on the GPU is a GPU fault: the
//     supervisor publishes notice.EngineCPUFallback and runs every later
//     worker on the CPU (LoadSpec.CPU, gpu = -1). Errors the engine reports,
//     such as a damaged model file, are not faults; they are passed on.
//   - Cancelling a Recognize gives the decode a moment to finish, then ends
//     the worker, since NeMo-Speech.cpp cannot stop a decode.
//   - The worker's stderr, its own JSON log and the native engine's output,
//     is re-logged through slog with the worker's process ID as the "worker"
//     attribute; the last lines go into the log when it crashes.
//
// The package has no CGo: the worker side hosts any engine.Engine, and the
// tests run the test binary itself as a worker hosting engine/fake.
package worker
