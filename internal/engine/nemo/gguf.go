package nemo

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// A minimal reader for the key-value metadata at the start of a GGUF file
// (https://github.com/ggml-org/ggml/blob/master/docs/gguf.md). The binding
// reads it to learn what a model can do before loading it, because the C ABI
// does not report that.

// errNotGGUF means the file is not a GGUF file, or not one this reader
// understands.
var errNotGGUF = errors.New("not a GGUF file")

// Limits that keep a corrupt or hostile file from making the reader allocate
// without bound.
const (
	ggufMaxString = 1 << 20  // bytes in one string
	ggufMaxArray  = 1 << 24  // elements in one array
	ggufMaxKeys   = 1 << 16  // metadata entries
	ggufMaxKept   = 64 << 20 // bytes of kept arrays in all
)

// GGUF metadata value types.
const (
	ggufUint8 uint32 = iota
	ggufInt8
	ggufUint16
	ggufInt16
	ggufUint32
	ggufInt32
	ggufFloat32
	ggufBool
	ggufString
	ggufArray
	ggufUint64
	ggufInt64
	ggufFloat64
)

// ggufMeta maps metadata keys to values: uint64 for unsigned integers, int64
// for signed ones, float64, bool, string, or []any for arrays.
type ggufMeta map[string]any

// readGGUFMetaFile reads the metadata of the GGUF file at path, keeping the
// keys keep accepts (all of them when keep is nil).
func readGGUFMetaFile(path string, keep func(key string) bool) (ggufMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readGGUFMeta(bufio.NewReaderSize(f, 1<<16), keep)
}

// readGGUFMeta reads GGUF metadata (versions 2 and 3) from r, which is
// positioned at the start of the file. It stops before the tensor infos.
func readGGUFMeta(r io.Reader, keep func(key string) bool) (ggufMeta, error) {
	g := &ggufReader{r: r}
	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil || string(magic[:]) != "GGUF" {
		return nil, errNotGGUF
	}
	version := g.u32()
	if g.err == nil && version != 2 && version != 3 {
		return nil, fmt.Errorf("%w: version %d", errNotGGUF, version)
	}
	_ = g.u64() // tensor count
	n := g.u64()
	if g.err == nil && n > ggufMaxKeys {
		return nil, fmt.Errorf("%w: %d metadata keys", errNotGGUF, n)
	}
	meta := ggufMeta{}
	for i := uint64(0); i < n && g.err == nil; i++ {
		key := g.str()
		typ := g.u32()
		if g.err != nil {
			break
		}
		if keep == nil || keep(key) {
			meta[key] = g.value(typ, true)
		} else {
			g.value(typ, false)
		}
	}
	if g.err != nil {
		if errors.Is(g.err, io.EOF) || errors.Is(g.err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("%w: truncated metadata", errNotGGUF)
		}
		return nil, g.err
	}
	return meta, nil
}

// ggufReader decodes little-endian GGUF values, remembering the first error.
type ggufReader struct {
	r    io.Reader
	err  error
	kept int // bytes of kept arrays so far
	buf  [8]byte
}

func (g *ggufReader) read(n int) []byte {
	if g.err != nil {
		return make([]byte, n)
	}
	if _, err := io.ReadFull(g.r, g.buf[:n]); err != nil {
		g.err = err
	}
	return g.buf[:n]
}

func (g *ggufReader) fail(format string, args ...any) {
	if g.err == nil {
		g.err = fmt.Errorf("%w: "+format, append([]any{errNotGGUF}, args...)...)
	}
}

func (g *ggufReader) u32() uint32 { return binary.LittleEndian.Uint32(g.read(4)) }
func (g *ggufReader) u64() uint64 { return binary.LittleEndian.Uint64(g.read(8)) }

func (g *ggufReader) str() string {
	n := g.u64()
	if g.err != nil {
		return ""
	}
	if n > ggufMaxString {
		g.fail("string of %d bytes", n)
		return ""
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(g.r, b); err != nil {
		g.err = err
		return ""
	}
	return string(b)
}

func (g *ggufReader) skip(n uint64) {
	if g.err != nil {
		return
	}
	if _, err := io.CopyN(io.Discard, g.r, int64(n)); err != nil {
		g.err = err
	}
}

// value reads one value of type typ, returning it when keep is set and
// discarding it otherwise.
func (g *ggufReader) value(typ uint32, keep bool) any {
	switch typ {
	case ggufUint8:
		return uint64(g.read(1)[0])
	case ggufInt8:
		return int64(int8(g.read(1)[0]))
	case ggufUint16:
		return uint64(binary.LittleEndian.Uint16(g.read(2)))
	case ggufInt16:
		return int64(int16(binary.LittleEndian.Uint16(g.read(2))))
	case ggufUint32:
		return uint64(g.u32())
	case ggufInt32:
		return int64(int32(g.u32()))
	case ggufFloat32:
		return float64(math.Float32frombits(g.u32()))
	case ggufBool:
		return g.read(1)[0] != 0
	case ggufString:
		if !keep {
			if n := g.u64(); n > ggufMaxString {
				g.fail("string of %d bytes", n)
			} else {
				g.skip(n)
			}
			return nil
		}
		return g.str()
	case ggufArray:
		return g.array(keep)
	case ggufUint64:
		return g.u64()
	case ggufInt64:
		return int64(g.u64())
	case ggufFloat64:
		return math.Float64frombits(g.u64())
	default:
		g.fail("unknown value type %d", typ)
		return nil
	}
}

func (g *ggufReader) array(keep bool) any {
	typ := g.u32()
	n := g.u64()
	if g.err != nil {
		return nil
	}
	if n > ggufMaxArray {
		g.fail("array of %d elements", n)
		return nil
	}
	if !keep {
		if size := ggufFixedSize(typ); size > 0 {
			g.skip(n * size)
			return nil
		}
		for i := uint64(0); i < n && g.err == nil; i++ {
			g.value(typ, false)
		}
		return nil
	}
	if g.kept += int(n); g.kept > ggufMaxKept {
		g.fail("too much array data")
		return nil
	}
	out := make([]any, 0, n)
	for i := uint64(0); i < n && g.err == nil; i++ {
		out = append(out, g.value(typ, true))
	}
	return out
}

// ggufFixedSize is the encoded size of a fixed-size value type, or 0.
func ggufFixedSize(typ uint32) uint64 {
	switch typ {
	case ggufUint8, ggufInt8, ggufBool:
		return 1
	case ggufUint16, ggufInt16:
		return 2
	case ggufUint32, ggufInt32, ggufFloat32:
		return 4
	case ggufUint64, ggufInt64, ggufFloat64:
		return 8
	default:
		return 0
	}
}

func (m ggufMeta) str(key string) string {
	s, _ := m[key].(string)
	return s
}

func (m ggufMeta) boolean(key string) bool {
	b, _ := m[key].(bool)
	return b
}

func (m ggufMeta) strings(key string) []string {
	vals, _ := m[key].([]any)
	var out []string
	for _, v := range vals {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
