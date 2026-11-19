package goscriber_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"time"

	"github.com/opragmatico/goscriber"
	"github.com/opragmatico/goscriber/hook"
)

// Transcribe a voice memo with the engine of an installed goscriber binary.
func Example() {
	scriber, err := goscriber.New(goscriber.Options{
		Model:      "parakeet-tdt-0.6b-v2",
		Executable: "/Applications/Goscriber.app/Contents/MacOS/goscriber",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer scriber.Close()

	tr, err := scriber.TranscribeFile(context.Background(), "memo.flac",
		goscriber.OnProgress(func(done, total time.Duration) {
			log.Printf("%v of %v", done, total)
		}))
	switch {
	case errors.Is(err, goscriber.ErrNoSpeech):
		fmt.Println("(silence)")
	case err != nil:
		log.Fatal(err)
	default:
		fmt.Println(tr.Text)
	}
}

// Rewrite a transcript with the transcription hook installed for the Goscriber
// app, as the app rewrites a dictation before pasting it. The hook is a step
// of its own, so a hook that fails costs the transcript nothing.
func ExampleTranscript_HookInfo() {
	scriber, err := goscriber.New(goscriber.Options{})
	if err != nil {
		log.Fatal(err)
	}
	defer scriber.Close()

	ctx := context.Background()
	tr, err := scriber.TranscribeFile(ctx, "memo.wav")
	if err != nil {
		log.Fatal(err)
	}
	text := tr.Text
	if h, err := hook.Default(); err == nil {
		if out, err := h.Rewrite(ctx, text, tr.HookInfo()); err == nil {
			text = out
		} else {
			log.Printf("the hook failed, keeping the transcript: %v", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		log.Printf("the hook cannot run: %v", err)
	}
	fmt.Println(text)
}
