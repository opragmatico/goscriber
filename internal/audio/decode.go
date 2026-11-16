package audio

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mewkiz/flac"
)

var (
	// ErrUnsupportedFile means a file is not one ReadFile can decode.
	ErrUnsupportedFile = errors.New("not a WAV or FLAC file")
	// ErrMP3 means a file is MP3, which ReadFile does not decode: the one
	// pure-Go decoder, go-mp3, garbles some common low-bitrate files
	// without an error.
	ErrMP3 = errors.New("MP3 is not supported; convert the file to WAV or FLAC")
)

// FileExtensions are the extensions of the files ReadFile reads, for file
// pickers. ReadFile itself goes by the content.
var FileExtensions = []string{".wav", ".flac"}

// ReadFile reads an audio file into mono samples in [-1, 1] at the file's
// own sample rate, averaging the channels: WAV (as ReadWAV) or FLAC, told
// apart by their content rather than their name.
func ReadFile(path string) (samples []float32, rate int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	samples, rate, err = Read(f)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}
	return samples, rate, nil
}

// Read decodes a WAV or FLAC stream; see ReadFile.
func Read(r io.Reader) (samples []float32, rate int, err error) {
	br := bufio.NewReaderSize(r, 64<<10)
	head, _ := br.Peek(12)
	switch {
	case len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WAVE":
		return ReadWAV(br)
	case bytes.HasPrefix(head, []byte("fLaC")):
		return readFLAC(br)
	case bytes.HasPrefix(head, []byte("ID3")):
		// An ID3v2 tag leads most MP3 files, and some FLAC ones, which the
		// FLAC decoder skips.
		if s, rate, err := readFLAC(br); err == nil {
			return s, rate, nil
		}
		return nil, 0, fmt.Errorf("%w (a tagged file that is not FLAC, likely MP3)", ErrUnsupportedFile)
	case isMP3(head):
		return nil, 0, ErrMP3
	}
	return nil, 0, ErrUnsupportedFile
}

// isMP3 reports whether b starts with an MPEG audio frame header: an 11-bit
// sync, then a valid version and layer III.
func isMP3(b []byte) bool {
	return len(b) >= 2 && b[0] == 0xFF && b[1]&0xE0 == 0xE0 && b[1]&0x18 != 0x08 && b[1]&0x06 == 0x02
}

// readFLAC decodes a FLAC stream.
func readFLAC(r io.Reader) ([]float32, int, error) {
	stream, err := flac.New(r)
	if err != nil {
		return nil, 0, fmt.Errorf("FLAC: %w", err)
	}
	defer stream.Close()
	info := stream.Info
	if info.SampleRate == 0 || info.NChannels == 0 || info.BitsPerSample == 0 {
		return nil, 0, fmt.Errorf("FLAC: %d channels of %d bits at %d Hz", info.NChannels, info.BitsPerSample, info.SampleRate)
	}
	var out []float32
	if info.NSamples > 0 && info.NSamples < 1<<32 {
		out = make([]float32, 0, info.NSamples)
	}
	for {
		frame, err := stream.ParseNext()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("FLAC: %w", err)
		}
		if len(frame.Subframes) == 0 {
			continue
		}
		bits := frame.BitsPerSample
		if bits == 0 {
			bits = info.BitsPerSample
		}
		scale := 1 / (float32(int64(1)<<(bits-1)) * float32(len(frame.Subframes)))
		n := len(frame.Subframes[0].Samples)
		for i := range n {
			var sum int64
			for _, sub := range frame.Subframes {
				if i < len(sub.Samples) {
					sum += int64(sub.Samples[i])
				}
			}
			out = append(out, float32(sum)*scale)
		}
	}
	return out, int(info.SampleRate), nil
}
