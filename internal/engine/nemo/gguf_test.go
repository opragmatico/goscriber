package nemo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/opragmatico/goscriber/internal/engine"
)

// ggufWriter builds GGUF headers for tests.
type ggufWriter struct {
	buf  bytes.Buffer
	keys int
	body bytes.Buffer
}

func (w *ggufWriter) le(v any) { _ = binary.Write(&w.body, binary.LittleEndian, v) }

func (w *ggufWriter) str(s string) {
	w.le(uint64(len(s)))
	w.body.WriteString(s)
}

func (w *ggufWriter) kv(key string, typ uint32, write func()) *ggufWriter {
	w.keys++
	w.str(key)
	w.le(typ)
	write()
	return w
}

func (w *ggufWriter) String(key, v string) *ggufWriter {
	return w.kv(key, ggufString, func() { w.str(v) })
}

func (w *ggufWriter) Bool(key string, v bool) *ggufWriter {
	return w.kv(key, ggufBool, func() { w.le(v) })
}

func (w *ggufWriter) Strings(key string, vs ...string) *ggufWriter {
	return w.kv(key, ggufArray, func() {
		w.le(ggufString)
		w.le(uint64(len(vs)))
		for _, v := range vs {
			w.str(v)
		}
	})
}

func (w *ggufWriter) bytes(version uint32) []byte {
	w.buf.Reset()
	w.buf.WriteString("GGUF")
	_ = binary.Write(&w.buf, binary.LittleEndian, version)
	_ = binary.Write(&w.buf, binary.LittleEndian, uint64(7)) // tensors, never read
	_ = binary.Write(&w.buf, binary.LittleEndian, uint64(w.keys))
	w.buf.Write(w.body.Bytes())
	w.buf.WriteString("tensor infos follow")
	return w.buf.Bytes()
}

func (w *ggufWriter) file(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(path, w.bytes(3), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadGGUFMetaValues(t *testing.T) {
	w := &ggufWriter{}
	w.kv("u8", ggufUint8, func() { w.le(uint8(200)) })
	w.kv("i8", ggufInt8, func() { w.le(int8(-3)) })
	w.kv("u16", ggufUint16, func() { w.le(uint16(60000)) })
	w.kv("i16", ggufInt16, func() { w.le(int16(-300)) })
	w.kv("u32", ggufUint32, func() { w.le(uint32(70000)) })
	w.kv("i32", ggufInt32, func() { w.le(int32(-70000)) })
	w.kv("f32", ggufFloat32, func() { w.le(float32(0.5)) })
	w.kv("u64", ggufUint64, func() { w.le(uint64(math.MaxUint64)) })
	w.kv("i64", ggufInt64, func() { w.le(int64(-1)) })
	w.kv("f64", ggufFloat64, func() { w.le(2.25) })
	w.Bool("yes", true).String("name", "nemotron")
	w.kv("ints", ggufArray, func() {
		w.le(ggufInt32)
		w.le(uint64(3))
		w.le([]int32{70, 13, -1})
	})
	w.kv("nested", ggufArray, func() {
		w.le(ggufArray)
		w.le(uint64(1))
		w.le(ggufBool)
		w.le(uint64(2))
		w.le([]bool{true, false})
	})

	for _, version := range []uint32{2, 3} {
		got, err := readGGUFMeta(bytes.NewReader(w.bytes(version)), nil)
		if err != nil {
			t.Fatalf("version %d: %v", version, err)
		}
		want := ggufMeta{
			"u8": uint64(200), "i8": int64(-3), "u16": uint64(60000), "i16": int64(-300),
			"u32": uint64(70000), "i32": int64(-70000), "f32": 0.5, "u64": uint64(math.MaxUint64),
			"i64": int64(-1), "f64": 2.25, "yes": true, "name": "nemotron",
			"ints":   []any{int64(70), int64(13), int64(-1)},
			"nested": []any{[]any{true, false}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("version %d:\n got %v\nwant %v", version, got, want)
		}
	}
}

func TestReadGGUFMetaKeepsOnlyWantedKeys(t *testing.T) {
	w := (&ggufWriter{}).
		Strings("tokenizer.ggml.tokens", "a", "b", "c").
		String("general.architecture", "asr")
	w.kv("scores", ggufArray, func() {
		w.le(ggufFloat32)
		w.le(uint64(4))
		w.le([]float32{1, 2, 3, 4})
	})
	w.Bool("asr.encoder.cache_supported", true)

	got, err := readGGUFMeta(bytes.NewReader(w.bytes(3)), func(k string) bool { return k != "tokenizer.ggml.tokens" && k != "scores" })
	if err != nil {
		t.Fatal(err)
	}
	want := ggufMeta{"general.architecture": "asr", "asr.encoder.cache_supported": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReadGGUFMetaRejectsBadInput(t *testing.T) {
	good := (&ggufWriter{}).String("general.architecture", "asr").bytes(3)
	hugeString := &ggufWriter{}
	hugeString.kv("k", ggufString, func() { hugeString.le(uint64(ggufMaxString + 1)) })
	badType := &ggufWriter{}
	badType.kv("k", 99, func() {})

	for name, data := range map[string][]byte{
		"empty":         nil,
		"wrong magic":   []byte("GGML\x03\x00\x00\x00"),
		"version 1":     append([]byte("GGUF"), 1, 0, 0, 0),
		"truncated":     good[:len(good)-25],
		"huge string":   hugeString.bytes(3),
		"unknown type":  badType.bytes(3),
		"too many keys": append(append([]byte("GGUF"), 3, 0, 0, 0), binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint64(nil, 0), ggufMaxKeys+1)...),
	} {
		if _, err := readGGUFMeta(bytes.NewReader(data), nil); !errors.Is(err, errNotGGUF) {
			t.Errorf("%s: err = %v, want errNotGGUF", name, err)
		}
	}
}

func TestInspectModel(t *testing.T) {
	nemotron := (&ggufWriter{}).
		String("general.architecture", "asr").
		Bool("asr.encoder.cache_supported", true).
		Strings("asr.rnnt.prompt_dictionary", "en-US:0", "es-ES:1", "auto:40", "de-DE:2", "malformed").
		file(t)
	meta, err := inspectModel("load", nemotron)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.streaming || !slices.Equal(meta.languages, []string{"auto", "de-DE", "en-US", "es-ES"}) {
		t.Errorf("Nemotron: %+v", meta)
	}

	tdt := (&ggufWriter{}).String("general.architecture", "asr").Bool("asr.encoder.cache_supported", false).file(t)
	if meta, err := inspectModel("load", tdt); err != nil || meta.streaming || meta.languages != nil {
		t.Errorf("TDT: %+v, %v", meta, err)
	}
}

func TestInspectModelErrors(t *testing.T) {
	dir := t.TempDir()
	notGGUF := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notGGUF, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	transcribeCpp := (&ggufWriter{}).String("general.architecture", "parakeet").file(t)

	for name, tc := range map[string]struct {
		path string
		kind error
	}{
		"missing":          {filepath.Join(dir, "missing.gguf"), engine.ErrModelNotFound},
		"directory":        {dir, engine.ErrInvalidArgument},
		"not GGUF":         {notGGUF, engine.ErrInvalidArgument},
		"other model kind": {transcribeCpp, engine.ErrInvalidArgument},
	} {
		_, err := inspectModel("load", tc.path)
		if !errors.Is(err, tc.kind) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.kind)
		}
	}
}
