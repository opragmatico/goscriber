// Package engine defines the speech-recognition interfaces (Engine, Stream,
// Result) that the rest of Goscriber uses. It has no CGo: the NeMo-Speech.cpp
// binding lives in engine/nemo and the crash-isolating worker in engine/worker.
package engine
