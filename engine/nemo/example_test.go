//go:build cgo && nemo

package nemo_test

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/opragmatico/goscriber/engine"
	"github.com/opragmatico/goscriber/engine/nemo"
)

// Transcribe a whole clip with an offline model such as Parakeet TDT.
func Example() {
	ctx := context.Background()
	e := nemo.New()
	info, err := e.Load(ctx, engine.LoadSpec{
		ID:   "parakeet-tdt-0.6b-v2",
		Path: "/path/to/parakeet-tdt-0.6b-v2.q8_0.gguf",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer e.Unload()
	fmt.Println("streaming:", info.Streaming, "on the CPU:", info.CPU)

	var samples []float32 // mono, in [-1, 1], from a file or a microphone
	res, err := e.Recognize(ctx, samples, 16000, engine.Options{WordTimestamps: true})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Text)
	for _, w := range res.Words {
		fmt.Printf("%v–%v %s\n", w.Start, w.End, w.Text)
	}
}

// Decode audio as it arrives with a streaming model such as Nemotron.
func Example_stream() {
	ctx := context.Background()
	e := nemo.New()
	if _, err := e.Load(ctx, engine.LoadSpec{
		ID:      "nemotron-3.5-asr-streaming-0.6b",
		Path:    "/path/to/nemotron-3.5-asr-streaming-0.6b.q8_0.gguf",
		Latency: engine.LatencyBalanced,
	}); err != nil {
		log.Fatal(err)
	}
	defer e.Unload()

	s, err := e.StartStream(ctx, engine.Options{InterimResults: true})
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	var chunks [][]float32 // 20 ms of 16 kHz mono audio each, as captured
	for _, chunk := range chunks {
		if err := s.Push(chunk, 16000); err != nil {
			log.Fatal(err)
		}
		for r, err := s.Next(); r != nil && err == nil; r, err = s.Next() {
			fmt.Println("interim:", r.Text)
		}
	}
	if err := s.Finish(); err != nil {
		log.Fatal(err)
	}
	for {
		r, err := s.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		if r != nil && r.Final {
			fmt.Println("final:", r.Text)
		}
	}
}
