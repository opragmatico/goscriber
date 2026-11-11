package nemo

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode"
)

// Test fixtures shared with the rest of the repository (testdata/README.md).
const (
	fixtureWAV  = "../../../testdata/audio/jfk.wav"
	fixtureText = "../../../testdata/audio/jfk.txt"
)

// readWAV reads a PCM 16-bit WAV file as mono float32 samples, averaging the
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
				return nil, 0, fmt.Errorf("%s: want 16-bit PCM before the data, got %d bits, %d channels", path, bits, channels)
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

// fixture returns the shared test clip and its reference transcript.
func fixture(t testing.TB) (samples []float32, rate int, transcript string) {
	t.Helper()
	samples, rate, err := readWAV(fixtureWAV)
	if err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(fixtureText)
	if err != nil {
		t.Fatal(err)
	}
	return samples, rate, strings.TrimSpace(string(text))
}

// wer is the word error rate of hyp against ref, ignoring case and
// punctuation: word-level edit distance over the reference length.
func wer(ref, hyp string) float64 {
	r, h := normalizeWords(ref), normalizeWords(hyp)
	if len(r) == 0 {
		if len(h) == 0 {
			return 0
		}
		return 1
	}
	prev := make([]int, len(h)+1)
	cur := make([]int, len(h)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(r); i++ {
		cur[0] = i
		for j := 1; j <= len(h); j++ {
			sub := prev[j-1]
			if r[i-1] != h[j-1] {
				sub++
			}
			cur[j] = min(sub, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return float64(prev[len(h)]) / float64(len(r))
}

func normalizeWords(s string) []string {
	return strings.Fields(strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '\'':
			return unicode.ToLower(r)
		default:
			return ' '
		}
	}, s))
}

func TestWER(t *testing.T) {
	for _, tc := range []struct {
		ref, hyp string
		want     float64
	}{
		{"Ask not what your country can do.", "ask not, what your country can do", 0},
		{"a b c d", "a x c d", 0.25},
		{"a b c d", "a b c", 0.25},
		{"a b", "a b c d", 1},
		{"", "", 0},
		{"", "x", 1},
	} {
		if got := wer(tc.ref, tc.hyp); got != tc.want {
			t.Errorf("wer(%q, %q) = %v, want %v", tc.ref, tc.hyp, got, tc.want)
		}
	}
}

func TestReadWAVFixture(t *testing.T) {
	samples, rate, text := fixture(t)
	if rate != 16000 || len(samples) != 176000 || text == "" {
		t.Errorf("fixture: %d samples at %d Hz, transcript %q", len(samples), rate, text)
	}
	if _, _, err := readWAV(fixtureText); err == nil {
		t.Error("readWAV accepted a text file")
	}
}
