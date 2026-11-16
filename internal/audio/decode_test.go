package audio

import (
	"bytes"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The FLAC fixtures are one second of a 440 Hz sine at ffmpeg's default
// amplitude of 1/8, made with:
//
//	ffmpeg -f lavfi -i "sine=frequency=440:sample_rate=22050:duration=1" -ac 2 -sample_fmt s16 tone-stereo.flac
//	ffmpeg -f lavfi -i "sine=frequency=440:sample_rate=16000:duration=1" -ac 1 -sample_fmt s32 -bits_per_raw_sample 24 tone-24bit.flac
//
// ffmpeg's upmix to stereo lowers each channel by 3 dB.
func TestReadFileDecodesEachFormat(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "tone.wav")
	if err := WriteWAVFile(wav, sine(16000, 16000, 0.125, 440), 16000); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		file string
		rate int
		amp  float64
	}{
		{wav, 16000, 0.125},
		{"testdata/tone-stereo.flac", 22050, 0.125 / math.Sqrt2},
		{"testdata/tone-24bit.flac", 16000, 0.125},
	} {
		t.Run(filepath.Base(tt.file), func(t *testing.T) {
			samples, rate, err := ReadFile(tt.file)
			if err != nil {
				t.Fatal(err)
			}
			if rate != tt.rate || len(samples) != rate {
				t.Errorf("%d samples at %d Hz, want one second at %d Hz", len(samples), rate, tt.rate)
			}
			// A sine's RMS is its amplitude over √2, and it crosses zero
			// twice per cycle.
			if rms, want := rmsOf(samples), tt.amp/math.Sqrt2; math.Abs(rms-want) > 0.005 {
				t.Errorf("RMS %.4f, want about %.4f: the samples are scaled wrong", rms, want)
			}
			if f := crossings(samples) / 2; math.Abs(f-440) > 5 {
				t.Errorf("about %.0f Hz, want 440", f)
			}
		})
	}
}

func TestReadRefusesOtherFiles(t *testing.T) {
	for _, tt := range []struct {
		data []byte
		want error
	}{
		{[]byte("hello, world"), ErrUnsupportedFile},
		{nil, ErrUnsupportedFile},
		{[]byte("OggS\x00\x02\x00\x00"), ErrUnsupportedFile},
		{[]byte("ID3\x04\x00\x00\x00\x00\x00\x00\xff\xf3\x40\x64"), ErrUnsupportedFile},
		{[]byte("\xff\xfb\x90\x64\x00\x00"), ErrMP3},
		{[]byte("\xff\xf3\x40\xc4\x00\x00"), ErrMP3},
	} {
		if _, _, err := Read(bytes.NewReader(tt.data)); !errors.Is(err, tt.want) {
			t.Errorf("Read(%q): %v, want %v", tt.data, err, tt.want)
		}
	}
	path := filepath.Join(t.TempDir(), "notes.wav")
	if err := os.WriteFile(path, []byte("not audio at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadFile(path); !errors.Is(err, ErrUnsupportedFile) || !strings.Contains(err.Error(), "notes.wav") {
		t.Errorf("ReadFile: %v, want ErrUnsupportedFile naming the file", err)
	}
}

func TestReadFLACAfterAnID3Tag(t *testing.T) {
	flacData, err := os.ReadFile("testdata/tone-24bit.flac")
	if err != nil {
		t.Fatal(err)
	}
	// An ID3v2.4 header with a 5-byte body, as some taggers put on FLAC.
	tagged := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x05hello"), flacData...)
	samples, rate, err := Read(bytes.NewReader(tagged))
	if err != nil {
		t.Fatal(err)
	}
	if rate != 16000 || len(samples) != 16000 {
		t.Errorf("%d samples at %d Hz, want 16000 at 16000", len(samples), rate)
	}
}

func rmsOf(s []float32) float64 {
	var sum float64
	for _, v := range s {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(s)))
}

func crossings(s []float32) float64 {
	n := 0
	for i := 1; i < len(s); i++ {
		if (s[i-1] < 0) != (s[i] < 0) {
			n++
		}
	}
	return float64(n)
}
