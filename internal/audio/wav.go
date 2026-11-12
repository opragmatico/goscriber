package audio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// ErrNotWAV means the data is not a RIFF/WAVE file, or not one ReadWAV can
// decode.
var ErrNotWAV = errors.New("not a supported WAV file")

// WAV format tags.
const (
	wavPCM        = 0x0001
	wavFloat      = 0x0003
	wavExtensible = 0xFFFE
)

// ReadWAVFile reads the WAV file at path; see ReadWAV.
func ReadWAVFile(path string) (samples []float32, rate int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	samples, rate, err = ReadWAV(f)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}
	return samples, rate, nil
}

// ReadWAV decodes a WAV stream into mono samples in [-1, 1] at the file's own
// sample rate, averaging the channels. It reads integer PCM of 8, 16, 24 or 32
// bits and IEEE float of 32 or 64 bits, plain or in WAVE_FORMAT_EXTENSIBLE.
// A data chunk whose size runs past the end of the stream, as some recorders
// leave it, is read to the end.
func ReadWAV(r io.Reader) (samples []float32, rate int, err error) {
	br := bufio.NewReaderSize(r, 64<<10)
	var hdr [12]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil || string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("%w: no RIFF/WAVE header", ErrNotWAV)
	}
	var f *wavFormat
	for {
		var ch [8]byte
		if _, err := io.ReadFull(br, ch[:]); err != nil {
			return nil, 0, fmt.Errorf("%w: no data chunk", ErrNotWAV)
		}
		id, size := string(ch[0:4]), binary.LittleEndian.Uint32(ch[4:8])
		switch id {
		case "fmt ":
			if size < 16 || size > 1<<10 {
				return nil, 0, fmt.Errorf("%w: fmt chunk of %d bytes", ErrNotWAV, size)
			}
			body := make([]byte, size+size%2)
			if _, err := io.ReadFull(br, body); err != nil {
				return nil, 0, fmt.Errorf("%w: truncated fmt chunk", ErrNotWAV)
			}
			if f, err = parseFormat(body[:size]); err != nil {
				return nil, 0, err
			}
		case "data":
			if f == nil {
				return nil, 0, fmt.Errorf("%w: data before fmt", ErrNotWAV)
			}
			samples, err := f.decode(br, int64(size))
			if err != nil {
				return nil, 0, err
			}
			return samples, f.rate, nil
		default:
			if _, err := br.Discard(int(size + size%2)); err != nil {
				return nil, 0, fmt.Errorf("%w: truncated %q chunk", ErrNotWAV, id)
			}
		}
	}
}

// wavFormat is what the fmt chunk says about the samples.
type wavFormat struct {
	tag      uint16 // wavPCM or wavFloat, after resolving the extensible format
	channels int
	rate     int
	bits     int // container bits per sample
}

func parseFormat(b []byte) (*wavFormat, error) {
	f := &wavFormat{
		tag:      binary.LittleEndian.Uint16(b[0:2]),
		channels: int(binary.LittleEndian.Uint16(b[2:4])),
		rate:     int(binary.LittleEndian.Uint32(b[4:8])),
		bits:     int(binary.LittleEndian.Uint16(b[14:16])),
	}
	if f.tag == wavExtensible {
		if len(b) < 40 {
			return nil, fmt.Errorf("%w: short WAVE_FORMAT_EXTENSIBLE header", ErrNotWAV)
		}
		f.tag = binary.LittleEndian.Uint16(b[24:26]) // the subformat GUID starts with the tag
	}
	ok := f.tag == wavPCM && (f.bits == 8 || f.bits == 16 || f.bits == 24 || f.bits == 32) ||
		f.tag == wavFloat && (f.bits == 32 || f.bits == 64)
	if !ok {
		return nil, fmt.Errorf("%w: format 0x%04x with %d bits per sample (want PCM 8, 16, 24 or 32 bits, or float 32 or 64 bits)", ErrNotWAV, f.tag, f.bits)
	}
	if f.channels < 1 || f.rate < 1 {
		return nil, fmt.Errorf("%w: %d channels at %d Hz", ErrNotWAV, f.channels, f.rate)
	}
	return f, nil
}

// decode reads up to size bytes of interleaved frames and mixes them down to
// mono. It stops early at the end of the stream and drops a final partial
// frame.
func (f *wavFormat) decode(r io.Reader, size int64) ([]float32, error) {
	width := f.bits / 8
	frame := width * f.channels
	sample := f.sampleFunc()
	scale := 1 / float32(f.channels)

	// A size of 0 or 0xFFFFFFFF is a placeholder left by a recorder that
	// could not seek back: read to the end.
	if size == 0 || size == 1<<32-1 {
		size = 0
	}
	var out []float32
	if size > 0 {
		out = make([]float32, 0, size/int64(frame))
	}
	buf := make([]byte, frame*4096)
	var rest int // bytes of a partial frame carried to the next read
	for read := int64(0); size <= 0 || read < size; {
		want := len(buf) - rest
		if size > 0 {
			want = int(min(int64(want), size-read))
		}
		n, err := io.ReadFull(r, buf[rest:rest+want])
		read += int64(n)
		n += rest
		whole := n - n%frame
		for p := 0; p < whole; p += frame {
			var sum float32
			for c := range f.channels {
				sum += sample(buf[p+c*width:])
			}
			out = append(out, sum*scale)
		}
		rest = copy(buf, buf[whole:n])
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break // the stream ended, possibly before the chunk said it would
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// sampleFunc returns a decoder for one sample in [-1, 1].
func (f *wavFormat) sampleFunc() func([]byte) float32 {
	switch {
	case f.tag == wavFloat && f.bits == 32:
		return func(b []byte) float32 { return math.Float32frombits(binary.LittleEndian.Uint32(b)) }
	case f.tag == wavFloat:
		return func(b []byte) float32 { return float32(math.Float64frombits(binary.LittleEndian.Uint64(b))) }
	case f.bits == 8: // unsigned
		return func(b []byte) float32 { return float32(int(b[0])-128) / 128 }
	case f.bits == 16:
		return func(b []byte) float32 { return float32(int16(binary.LittleEndian.Uint16(b))) / (1 << 15) }
	case f.bits == 24:
		return func(b []byte) float32 {
			v := int32(uint32(b[0])<<8|uint32(b[1])<<16|uint32(b[2])<<24) >> 8
			return float32(v) / (1 << 23)
		}
	default: // 32-bit integer
		return func(b []byte) float32 { return float32(int32(binary.LittleEndian.Uint32(b))) / (1 << 31) }
	}
}

// WriteWAVFile writes samples to a new file at path; see WriteWAV. The file
// is readable only by the user, as recordings are private.
func WriteWAVFile(path string, samples []float32, rate int) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := WriteWAV(f, samples, rate); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// WriteWAV writes mono samples in [-1, 1] as a 16-bit PCM WAV stream,
// rounding and clipping each sample.
func WriteWAV(w io.Writer, samples []float32, rate int) error {
	const maxData = 1<<32 - 1 - 36
	size := 2 * len(samples)
	if rate < 1 || size > maxData {
		return fmt.Errorf("audio: cannot write %d samples at %d Hz as WAV", len(samples), rate)
	}
	bw := bufio.NewWriterSize(w, 64<<10)
	var hdr [44]byte
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+size))
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], wavPCM)
	binary.LittleEndian.PutUint16(hdr[22:], 1)
	binary.LittleEndian.PutUint32(hdr[24:], uint32(rate))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(hdr[32:], 2)
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(size))
	if _, err := bw.Write(hdr[:]); err != nil {
		return err
	}
	var b [2]byte
	for _, s := range samples {
		v := math.Round(float64(s) * (1 << 15))
		binary.LittleEndian.PutUint16(b[:], uint16(int16(max(-(1<<15), min(1<<15-1, v)))))
		if _, err := bw.Write(b[:]); err != nil {
			return err
		}
	}
	return bw.Flush()
}
