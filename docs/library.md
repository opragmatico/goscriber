# Goscriber as a library

Two public packages let another Go program put Goscriber inside its own work, such as a workflow or an agent:

| Package | What it does | Needs |
| --- | --- | --- |
| [`github.com/opragmatico/goscriber`](../goscriber.go) | `Transcriber`: audio files, streams or samples to text, the way the app transcribes them | A model Goscriber downloaded, and a `goscriber` binary built with the engine (or the engine built in) |
| [`github.com/opragmatico/goscriber/hook`](../hook/hook.go) | The [transcription hook](architecture.md#transcription-hook), both ends: `Script` runs a hook program, `Main` makes a Go program one | Nothing but the standard library |

The two directions they cover:

- **A workflow uses Goscriber.** A workflow step transcribes a voice memo with `Transcriber`, and another rewrites the transcript with `hook.Script`, the same program the app runs before pasting.
- **Goscriber uses a workflow.** The app's transcription hook is a Go program built with `hook.Main` that hands every dictation to a workflow and prints its answer, which the app pastes.

[goagents](#with-goagents), the LangGraph-style layer on Temporal in agentic-ai-go, fits both without an adapter: a transcription is a node or a tool, and a hook program is a client.

## Transcribing

```go
scriber, err := goscriber.New(goscriber.Options{
	Model:      "parakeet-tdt-0.6b-v2",
	Executable: "/Applications/Goscriber.app/Contents/MacOS/goscriber",
})
if err != nil {
	return err
}
defer scriber.Close()

tr, err := scriber.TranscribeFile(ctx, "memo.flac",
	goscriber.OnProgress(func(done, total time.Duration) { log.Printf("%v of %v", done, total) }))
```

`Transcript` holds the text after Goscriber's cleanup (`Text`), the model's own (`RawText`), the model, the language as best known and the audio's length. Its fields have JSON names, so it can sit in a workflow's state as it is:

```json
{"text": "And so, my fellow Americans…", "raw_text": "And so, my fellow Americans…", "model": "parakeet-tdt-0.6b-v2", "language": "en", "duration_ns": 11000000000}
```

- **Input.** `TranscribeFile` takes a WAV or FLAC file at any sample rate, `TranscribeReader` the same from an `io.Reader` (an upload, an object from storage), and `Transcribe` mono samples at 8 to 96 kHz. Audio of any length is decoded the way the app decodes a long file: through a stream for Nemotron, in chunks cut at pauses for Parakeet ([transcribing files](architecture.md#transcribing-files)).
- **Settings.** Options left empty fall back to Goscriber's settings, as `goscriber transcribe` does: the selected model (else the catalog's default), the language, the custom words, filler-word removal. `Words` adds custom words; `Language` must be one the model takes. `GOSCRIBER_HOME` moves the settings and models, as for the app.
- **Models.** `New` checks that the model is downloaded and fails with `ErrNoModel` or `ErrModelCorrupt` otherwise; nothing is downloaded (`goscriber models pull <id>` does). The first transcription loads the model, which takes a few seconds, and it stays loaded until `Close`.
- **Concurrency.** A `Transcriber` is safe for concurrent use. Transcriptions take turns on the engine ten seconds of audio at a time, so a short one started during a long one does not wait for all of it. Create one per process and share it: each holds its own engine and model.
- **Cancellation and progress.** Cancelling the context stops a transcription between pieces of audio. `OnProgress` is called after every ten seconds of audio, on the transcribing goroutine.
- **Errors.** `ErrNoSpeech` (too short, too quiet, or no words heard), `ErrUnsupportedAudio` (not WAV or FLAC, or a sample rate outside the engine's), `ErrNoModel`, `ErrModelCorrupt` and `ErrNoEngine` are permanent: another attempt gets the same answer. An engine that crashed or hung is not: the next transcription starts it again.

### Where the engine runs

The engine is NeMo-Speech.cpp, a C++ library, so a `Transcriber` runs it one of two ways:

- **In a worker process** (the default): the `engine-worker` subcommand of a `goscriber` binary built with the engine (`make deps build`), named by `Options.Executable` or found in `PATH`. It is how the app runs the engine ([process isolation](architecture.md#process-isolation)): a crash ends the worker, not the program, and the next call starts another, on the CPU if the GPU was at fault. The program itself needs no CGo and builds with `CGO_ENABLED=0`. The binary must speak the same worker protocol as the package, which the worker's handshake checks, so use a binary from the same Goscriber version.
- **In process** (`Options.InProcess`): the program links the engine itself, which takes the `nemo` build tag, CGo and the SDK on `PKG_CONFIG_PATH` (`go build -tags nemo` with `PKG_CONFIG_PATH=<goscriber>/.deps/nemo-speech/lib/pkgconfig`). One binary to ship, but a crash in the engine ends the program.

## The transcription hook from Go

The [contract](architecture.md#transcription-hook) is Handy's: a program gets the transcript on standard input and prints the text to use instead; one trailing newline is dropped, empty output means nothing, a failure or a run past 30 seconds keeps the original. Package `hook` has both ends of it.

`hook.Script` runs a hook program, or any filter, as Goscriber does: with a timeout, the process group stopped with SIGTERM and then SIGKILL when it runs too long or the context ends, its output capped at 1 MiB and its failure in an `*hook.Error` with the end of its standard error. `hook.Default` is the hook installed for the app, if there is one:

```go
h, err := hook.Default() // fs.ErrNotExist when none is installed
if err == nil {
	text, err = h.Rewrite(ctx, tr.Text, tr.HookInfo())
}

opencc := hook.Script{Path: "/opt/homebrew/bin/opencc", Args: []string{"-c", "s2t.json"}}
```

`hook.Main` makes a Go program a hook. It reads the transcript, calls the `Hook`, prints its result and exits 0, or prints the error and exits 1 so that the app pastes the original. The hook's context ends on SIGTERM, which the app sends a second before it kills a hook that timed out or whose dictation was cancelled: time to end what the hook started elsewhere.

```go
func main() {
	hook.Main(hook.Func(func(ctx context.Context, text string, info hook.Info) (string, error) {
		return strings.ReplaceAll(text, "gonna", "going to"), nil
	}))
}
```

`hook.Info` carries the model and the transcript's language, which a script reads from `GOSCRIBER_MODEL` and `GOSCRIBER_LANGUAGE`.

## With goagents

The shapes line up: a goagents activity node is a `func(ctx, state) (graph.Result, error)` that may do any I/O, a tool is a `func(ctx, args) (string, error)`, and the state travels as JSON. Goscriber does not import goagents, and goagents need not know Goscriber: the workflow module requires both. The goagents module is named `goagents`, which `go get` cannot fetch, so a module that uses it points a `replace` directive at a checkout:

```text
require (
	github.com/opragmatico/goscriber v0.0.0
	goagents v0.0.0
)

replace (
	github.com/opragmatico/goscriber => ../goscriber
	goagents => ../agentic-ai-go/goagents
)
```

The examples below ran together on a Temporal dev server, with the `goscriber` binary from `make deps build` and Parakeet TDT 0.6B v2.

### A transcription node

Transcribe on an activity node, and keep the audio out of the state: a path or an object key travels through Temporal history, the audio would not fit. The progress callback is the node's heartbeat, so an idle timeout catches a stuck decode while a long file still has all the time it needs.

```go
type Memo struct {
	Audio      string               `json:"audio"`
	Transcript goscriber.Transcript `json:"transcript"`
	Text       string               `json:"text"`
}

func MemoGraph(scriber *goscriber.Transcriber, h hook.Hook) *graph.Graph[Memo] {
	return graph.New[Memo]("memo").
		Node("transcribe", func(ctx context.Context, s Memo) (graph.Result, error) {
			tr, err := scriber.TranscribeFile(ctx, s.Audio, goscriber.OnProgress(func(done, total time.Duration) {
				graph.Heartbeat(ctx, done.String())
			}))
			if err != nil {
				return nil, permanent(err)
			}
			return graph.Update{"Transcript": tr, "Text": tr.Text}, nil
		}, graph.Reads("Audio"), graph.Timeout(10*time.Minute), graph.IdleTimeout(time.Minute)).
		Node("rewrite", func(ctx context.Context, s Memo) (graph.Result, error) {
			text, err := h.Rewrite(ctx, s.Transcript.Text, s.Transcript.HookInfo())
			if err != nil {
				return nil, err
			}
			return graph.Update{"Text": text}, nil
		}, graph.Reads("Transcript"), graph.Timeout(hook.DefaultTimeout+5*time.Second)).
		Edge(graph.Start, "transcribe").
		Edge("transcribe", "rewrite").
		Edge("rewrite", graph.End).
		MustCompile()
}

// permanent stops the retries that cannot help.
func permanent(err error) error {
	if errors.Is(err, goscriber.ErrNoSpeech) || errors.Is(err, goscriber.ErrUnsupportedAudio) ||
		errors.Is(err, goscriber.ErrNoModel) || errors.Is(err, goscriber.ErrModelCorrupt) {
		return temporal.NewNonRetryableApplicationError(err.Error(), "goscriber", err)
	}
	return err
}
```

The hook is a node of its own rather than part of the transcription: it gets its own timeout and retries, a hook that fails never costs the transcript, and the checkpoint rows keep both texts. `h` can be `hook.Default()`, any `hook.Script`, or a `hook.Func`.

The worker creates the `Transcriber` once, before it starts, and closes it after it stops:

```go
scriber, err := goscriber.New(goscriber.Options{Model: "parakeet-tdt-0.6b-v2", Executable: goscriberBin})
if err != nil {
	log.Fatal(err)
}
defer scriber.Close()

rt := engine.New(store, MemoGraph(scriber, hook.Script{Path: "/usr/local/bin/tidy-memo"}))
w := worker.New(c, "voice", worker.Options{Plugins: []worker.Plugin{rt.Plugin()}})
```

### Transcription as a tool

```go
type TranscribeArgs struct {
	Path string `json:"path" description:"Absolute path of a WAV or FLAC file on the worker's disk"`
}

func TranscribeTool(scriber *goscriber.Transcriber) tool.Tool {
	return tool.Func("transcribe_audio", "Transcribe the speech in an audio file to text, offline.",
		func(ctx context.Context, a TranscribeArgs) (string, error) {
			tr, err := scriber.TranscribeFile(ctx, a.Path)
			if errors.Is(err, goscriber.ErrNoSpeech) {
				return "(no speech in the file)", nil
			}
			return tr.Text, err
		})
}

assistant := agent.New(chat, tool.NewSet(TranscribeTool(scriber)), agent.System("You answer questions about voice memos."))
```

The agent's tools node runs each call as an activity on the worker, so the call reaches the worker's `Transcriber`. A tool's error is the message the model reads next; `tool.FailStep()` on the tool makes it fail the step and retry instead.

### A dictation through a workflow

A hook program that is a goagents client sends every dictation through a graph, and the app pastes what comes back. Build it, then put it, or a symbolic link to it, at `hooks/transcription` in Goscriber's config directory:

```go
func main() {
	hook.Main(hook.Func(polish))
}

func polish(ctx context.Context, text string, info hook.Info) (string, error) {
	c, err := client.Dial(client.Options{HostPort: "localhost:7233"})
	if err != nil {
		return "", err
	}
	defer c.Close()
	ag := &engine.Client{Temporal: c, Store: checkpoint.NewMemory(), TaskQueue: "voice"}
	run, err := ag.Start(ctx, "polish", "dictation-"+uuid.NewString(), graph.Update{"Text": text, "Language": info.Language})
	if err != nil {
		return "", err
	}
	out, err := ag.Result(ctx, run, nil)
	if err != nil {
		if ctx.Err() != nil {
			// Goscriber gave up on the hook: end the run too.
			stop, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			_ = c.CancelWorkflow(stop, run.ThreadID, run.RunID)
		}
		return "", err
	}
	var s Polish
	if err := json.Unmarshal(out.State, &s); err != nil {
		return "", err
	}
	return s.Text, nil
}
```

The round trip took under 400 ms on a dev server with a node that rewrites in Go; a model call adds its own time. Mind the hook's 30 seconds: past them the app pastes the dictation as it was. The hook runs with the app's environment, not a shell's, so it names the Temporal address itself, or reads it from a file, rather than relying on variables set in `~/.zshrc`.

### Deploying

- **Workers.** Every worker that serves a graph with a transcription node needs the model files and the `goscriber` binary: goagents runs all of a graph's activity nodes on its task queue. To keep transcription on the one machine with a GPU, serve it as a graph of its own there and call it from the others with `graph.Remote`.
- **Memory.** A loaded model takes about a gigabyte. One `Transcriber` per worker process holds one.
- **Payloads.** Only the transcript passes through history: a ten-minute memo is a few kilobytes of text, far under Temporal's limits.
