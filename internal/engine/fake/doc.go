// Package fake provides a scripted engine.Engine for tests and for builds
// without the native engine.
//
// A Script fixes everything the engine does: the model Load reports, the
// transcripts Recognize returns, how long calls take, which calls fail, and
// the interim and final results each stream produces as audio arrives. The
// engine follows the engine.Engine contract the native one does (sample-rate
// rules, ErrNotLoaded before Load, ErrInvalidArgument for a stream on a model
// that cannot stream, io.EOF after the end-of-stream final), so code tested
// against it behaves the same with the real engine. It also records the
// calls it receives for tests to inspect.
//
//	eng := fake.New(fake.Script{
//		Model:   engine.ModelInfo{Streaming: true},
//		Streams: []fake.StreamScript{fake.Dictation("ask not what", 200*time.Millisecond)},
//	})
package fake
