package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// wavBytes builds a WAV file: a fmt chunk from the arguments, any extra
// chunks, then data.
func wavBytes(tag uint16, channels, rate, bits int, data []byte, extra ...[]byte) []byte {
	var fmtBody bytes.Buffer
	le := func(v any) { _ = binary.Write(&fmtBody, binary.LittleEndian, v) }
	le(tag)
	le(uint16(channels))
	le(uint32(rate))
	le(uint32(rate * channels * bits / 8))
	le(uint16(channels * bits / 8))
	le(uint16(bits))
	if tag == wavExtensible {
		le(uint16(22))
		le(uint16(bits))
		le(uint32(0))
		le(uint16(wavFloat)) // subformat GUID, KSDATAFORMAT_SUBTYPE_IEEE_FLOAT
		fmtBody.Write([]byte{0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71})
	}
	var body bytes.Buffer
	body.WriteString("WAVE")
	chunk := func(id string, b []byte) {
		body.WriteString(id)
		_ = binary.Write(&body, binary.LittleEndian, uint32(len(b)))
		body.Write(b)
		if len(b)%2 == 1 {
			body.WriteByte(0)
		}
	}
	for _, e := range extra {
		chunk("LIST", e)
	}
	chunk("fmt ", fmtBody.Bytes())
	chunk("data", data)
	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

func le16(vs ...int16) []byte {
	var b bytes.Buffer
	for _, v := range vs {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	return b.Bytes()
}

func near(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-4 }

func checkSamples(t *testing.T, got []float32, want ...float32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d samples %v, want %v", len(got), got, want)
	}
	for i := range want {
		if !near(got[i], want[i]) {
			t.Errorf("sample %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestReadWAVFixture(t *testing.T) {
	samples, rate, err := ReadWAVFile(filepath.Join("..", "..", "testdata", "audio", "jfk.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if rate != 16000 || len(samples) != 176000 {
		t.Fatalf("rate %d, %d samples; want 16000 Hz, 11.0 s", rate, len(samples))
	}
	var peak float32
	for _, s := range samples {
		peak = max(peak, float32(math.Abs(float64(s))))
	}
	if peak <= 0.1 || peak > 1 {
		t.Errorf("peak %v", peak)
	}
}

func TestReadWAVFormats(t *testing.T) {
	f32 := func(vs ...float32) []byte {
		var b bytes.Buffer
		for _, v := range vs {
			_ = binary.Write(&b, binary.LittleEndian, v)
		}
		return b.Bytes()
	}
	f64 := func(vs ...float64) []byte {
		var b bytes.Buffer
		for _, v := range vs {
			_ = binary.Write(&b, binary.LittleEndian, v)
		}
		return b.Bytes()
	}
	for _, tt := range []struct {
		name string
		wav  []byte
		rate int
		want []float32
	}{
		{"pcm16 mono", wavBytes(wavPCM, 1, 16000, 16, le16(0, 16384, -32768)), 16000, []float32{0, 0.5, -1}},
		{"pcm16 stereo is averaged", wavBytes(wavPCM, 2, 48000, 16, le16(16384, -16384, 32767, 32767)), 48000, []float32{0, 32767.0 / 32768}},
		{"pcm8", wavBytes(wavPCM, 1, 8000, 8, []byte{128, 192, 0}), 8000, []float32{0, 0.5, -1}},
		{"pcm24", wavBytes(wavPCM, 1, 22050, 24, []byte{0, 0, 0x40, 0, 0, 0x80}), 22050, []float32{0.5, -1}},
		{"pcm32", wavBytes(wavPCM, 1, 16000, 32, []byte{0, 0, 0, 0x40, 0, 0, 0, 0xC0}), 16000, []float32{0.5, -0.5}},
		{"float32 stereo at 44.1 kHz", wavBytes(wavFloat, 2, 44100, 32, f32(0.25, 0.75, -1, 1)), 44100, []float32{0.5, 0}},
		{"float64", wavBytes(wavFloat, 1, 96000, 64, f64(0.125, -0.5)), 96000, []float32{0.125, -0.5}},
		{"extensible float32", wavBytes(wavExtensible, 1, 16000, 32, f32(0.3)), 16000, []float32{0.3}},
		{"other chunks first, odd sized", wavBytes(wavPCM, 1, 16000, 16, le16(100), []byte("odd")), 16000, []float32{100.0 / 32768}},
		{"partial last frame dropped", wavBytes(wavPCM, 2, 16000, 16, append(le16(0, 0), 1, 2)), 16000, []float32{0}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, rate, err := ReadWAV(bytes.NewReader(tt.wav))
			if err != nil {
				t.Fatal(err)
			}
			if rate != tt.rate {
				t.Errorf("rate %d, want %d", rate, tt.rate)
			}
			checkSamples(t, got, tt.want...)
		})
	}
}

func TestReadWAVDataSizeBeyondTheEnd(t *testing.T) {
	wav := wavBytes(wavPCM, 1, 16000, 16, le16(1000, 2000, 3000))
	// A recorder that never went back to fix the sizes.
	binary.LittleEndian.PutUint32(wav[len(wav)-6-4:], 0xFFFFFFFF)
	got, _, err := ReadWAV(bytes.NewReader(wav))
	if err != nil {
		t.Fatal(err)
	}
	checkSamples(t, got, 1000.0/32768, 2000.0/32768, 3000.0/32768)

	// A size that is merely too large: read what is there.
	binary.LittleEndian.PutUint32(wav[len(wav)-6-4:], 1000)
	if got, _, err = ReadWAV(bytes.NewReader(wav)); err != nil || len(got) != 3 {
		t.Errorf("%d samples, %v", len(got), err)
	}
}

func TestReadWAVRejects(t *testing.T) {
	adpcm := wavBytes(0x0002, 1, 16000, 4, []byte{1, 2})
	noData := wavBytes(wavPCM, 1, 16000, 16, nil)
	noData = noData[:len(noData)-8] // drop the data chunk header
	dataFirst := []byte("RIFF\x10\x00\x00\x00WAVEdata\x00\x00\x00\x00")
	for name, data := range map[string][]byte{
		"empty":           nil,
		"not RIFF":        []byte("OggS and more bytes"),
		"ADPCM":           adpcm,
		"12-bit PCM":      wavBytes(wavPCM, 1, 16000, 12, nil),
		"zero channels":   wavBytes(wavPCM, 0, 16000, 16, nil),
		"no data chunk":   noData,
		"data before fmt": dataFirst,
	} {
		if _, _, err := ReadWAV(bytes.NewReader(data)); !errors.Is(err, ErrNotWAV) {
			t.Errorf("%s: err = %v, want ErrNotWAV", name, err)
		}
	}
}

func TestReadWAVFileErrors(t *testing.T) {
	if _, _, err := ReadWAVFile(filepath.Join(t.TempDir(), "missing.wav")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
	p := filepath.Join(t.TempDir(), "x.wav")
	_ = os.WriteFile(p, []byte("nope"), 0o600)
	if _, _, err := ReadWAVFile(p); !errors.Is(err, ErrNotWAV) || !bytes.Contains([]byte(err.Error()), []byte(p)) {
		t.Errorf("bad file: %v", err)
	}
}

func TestWriteWAVRoundTrip(t *testing.T) {
	in := []float32{0, 0.5, -0.5, 1, -1, 1.5, -2, 0.25}
	var buf bytes.Buffer
	if err := WriteWAV(&buf, in, 48000); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 44+2*len(in) {
		t.Errorf("%d bytes", buf.Len())
	}
	got, rate, err := ReadWAV(&buf)
	if err != nil || rate != 48000 {
		t.Fatalf("rate %d, %v", rate, err)
	}
	checkSamples(t, got, 0, 0.5, -0.5, 32767.0/32768, -1, 32767.0/32768, -1, 0.25)

	p := filepath.Join(t.TempDir(), "rec.wav")
	if err := WriteWAVFile(p, in, 16000); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if err := WriteWAV(&buf, in, 0); err == nil {
		t.Error("rate 0 accepted")
	}
}
