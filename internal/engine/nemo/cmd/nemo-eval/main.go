//go:build cgo && nemo

// Command nemo-eval transcribes audio files through the CGo binding and
// reports what a model evaluation needs: one JSON line per file with the
// reference, the hypothesis and the decode time, and a summary on stderr. It
// produced the numbers in docs/models/.
//
//	go run -tags nemo ./internal/engine/nemo/cmd/nemo-eval -model MODEL.gguf [flags] FILE.wav...
//	go run -tags nemo ./internal/engine/nemo/cmd/nemo-eval -model MODEL.gguf -manifest test.jsonl > results.jsonl
//
// A manifest is NeMo's format: JSON lines with "audio_filepath" (relative to
// the manifest) and "text", the reference transcript. The output lines use
// NeMo's names too, with the hypothesis in "pred_text", so scripts/wer.py can
// score them. Audio must be 16-bit PCM WAV; convert other formats first, for
// example with ffmpeg.
package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/opragmatico/goscriber/internal/engine"
	"github.com/opragmatico/goscriber/internal/engine/nemo"
)

type entry struct {
	Audio string `json:"audio_filepath"`
	Text  string `json:"text,omitempty"`
}

type word struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type result struct {
	Audio    string  `json:"audio_filepath"`
	Text     string  `json:"text,omitempty"`
	Pred     string  `json:"pred_text"`
	Duration float64 `json:"duration"`
	DecodeMS float64 `json:"decode_ms"`
	Words    []word  `json:"words,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "nemo-eval:", err)
		os.Exit(1)
	}
}

func run() error {
	model := flag.String("model", "", "GGUF model to load (required)")
	manifest := flag.String("manifest", "", "NeMo JSON-lines manifest of files and references")
	vad := flag.String("vad", "", "Silero VAD GGUF to mask silence with")
	cpu := flag.Bool("cpu", false, "run on the CPU instead of Metal")
	words := flag.Bool("words", false, "request word timestamps and include them in the output")
	runs := flag.Int("runs", 1, "decode each file this many times and keep the fastest")
	warmup := flag.Bool("warmup", true, "decode the first file once, untimed, before measuring")
	stream := flag.Bool("stream", false, "only try to open a stream, and report the result")
	flag.Parse()
	if *model == "" || *runs < 1 {
		flag.Usage()
		return errors.New("-model is required and -runs must be at least 1")
	}

	entries, err := inputs(*manifest, flag.Args())
	if err != nil {
		return err
	}
	if len(entries) == 0 && !*stream {
		return errors.New("no audio files: pass WAV files or -manifest")
	}

	ctx := context.Background()
	eng := nemo.New()
	start := time.Now()
	info, err := eng.Load(ctx, engine.LoadSpec{ID: filepath.Base(*model), Path: *model, VADPath: *vad, CPU: *cpu})
	if err != nil {
		return err
	}
	defer func() { _ = eng.Unload() }()
	backend := "metal"
	if *cpu {
		backend = "cpu"
	}
	fmt.Fprintf(os.Stderr, "loaded %s on %s in %v (streaming %t, languages %v)\n",
		info.ID, backend, time.Since(start).Round(time.Millisecond), info.Streaming, info.Languages)

	if *stream {
		s, err := eng.StartStream(ctx, engine.Options{InterimResults: true})
		if err != nil {
			fmt.Fprintf(os.Stderr, "stream refused: %v (ErrInvalidArgument: %t)\n", err, errors.Is(err, engine.ErrInvalidArgument))
			return nil
		}
		_ = s.Close()
		fmt.Fprintln(os.Stderr, "stream opened")
		return nil
	}

	opts := engine.Options{WordTimestamps: *words}
	if *warmup {
		samples, rate, err := readWAV(entries[0].Audio)
		if err != nil {
			return err
		}
		if _, err := eng.Recognize(ctx, samples, rate, opts); err != nil {
			return err
		}
	}

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	enc := json.NewEncoder(out)
	var audio, decode time.Duration
	for _, e := range entries {
		samples, rate, err := readWAV(e.Audio)
		if err != nil {
			return err
		}
		var best time.Duration
		var res engine.Result
		for i := range *runs {
			t := time.Now()
			r, err := eng.Recognize(ctx, samples, rate, opts)
			if err != nil {
				return fmt.Errorf("%s: %w", e.Audio, err)
			}
			if d := time.Since(t); i == 0 || d < best {
				best = d
			}
			res = r
		}
		dur := time.Duration(float64(len(samples)) / float64(rate) * float64(time.Second))
		audio += dur
		decode += best
		line := result{
			Audio:    e.Audio,
			Text:     e.Text,
			Pred:     res.Text,
			Duration: dur.Seconds(),
			DecodeMS: float64(best.Microseconds()) / 1000,
		}
		for _, w := range res.Words {
			line.Words = append(line.Words, word{Text: w.Text, Start: ms(w.Start), End: ms(w.End)})
		}
		if err := enc.Encode(line); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "%d files, %.1f s of audio decoded in %.2f s on %s: RTFx %.0f\n",
		len(entries), audio.Seconds(), decode.Seconds(), backend, audio.Seconds()/decode.Seconds())
	return nil
}

// ms returns d in seconds, rounded to the millisecond.
func ms(d time.Duration) float64 { return d.Round(time.Millisecond).Seconds() }

// inputs returns the manifest's entries, with their paths resolved against
// the manifest's directory, followed by the files named on the command line.
func inputs(manifest string, files []string) ([]entry, error) {
	var entries []entry
	if manifest != "" {
		f, err := os.Open(manifest)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		dec := json.NewDecoder(f)
		for {
			var e entry
			if err := dec.Decode(&e); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, fmt.Errorf("%s: %w", manifest, err)
			}
			if !filepath.IsAbs(e.Audio) {
				e.Audio = filepath.Join(filepath.Dir(manifest), e.Audio)
			}
			entries = append(entries, e)
		}
	}
	for _, f := range files {
		entries = append(entries, entry{Audio: f})
	}
	return entries, nil
}

// readWAV reads a 16-bit PCM WAV file as mono float32 samples, averaging the
// channels.
func readWAV(path string) (samples []float32, rate int, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("%s: not a WAV file", path)
	}
	var channels, bits int
	for p := 12; p+8 <= len(data); {
		id, size := string(data[p:p+4]), int(binary.LittleEndian.Uint32(data[p+4:p+8]))
		body := data[p+8 : min(p+8+size, len(data))]
		switch id {
		case "fmt ":
			if len(body) < 16 || binary.LittleEndian.Uint16(body[0:2]) != 1 {
				return nil, 0, fmt.Errorf("%s: not PCM", path)
			}
			channels = int(binary.LittleEndian.Uint16(body[2:4]))
			rate = int(binary.LittleEndian.Uint32(body[4:8]))
			bits = int(binary.LittleEndian.Uint16(body[14:16]))
		case "data":
			if bits != 16 || channels < 1 {
				return nil, 0, fmt.Errorf("%s: want 16-bit PCM, got %d bits, %d channels", path, bits, channels)
			}
			frames := len(body) / (2 * channels)
			samples = make([]float32, frames)
			for i := range samples {
				var sum float32
				for c := range channels {
					off := 2 * (i*channels + c)
					sum += float32(int16(binary.LittleEndian.Uint16(body[off:]))) / 32768
				}
				samples[i] = sum / float32(channels)
			}
			return samples, rate, nil
		}
		p += 8 + size + size%2
	}
	return nil, 0, fmt.Errorf("%s: no data chunk", path)
}
