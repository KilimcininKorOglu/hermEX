package ics

import (
	"encoding/binary"
	"fmt"
	"math"

	"hermex/internal/mapi"
)

// maxMVCount caps a decoded multivalue element count to reject a corrupt stream
// before allocating; legitimate multivalues are far smaller.
const maxMVCount = 1 << 20

func (r *reader) byte() (uint8, bool) {
	if r.pos+1 > len(r.b) {
		return 0, false
	}
	v := r.b[r.pos]
	r.pos++
	return v, true
}

// appendName writes a PROPERTY_NAME inline: the 16-byte GUID, the kind byte, and
// then either the LID (MnidID) or the name as naked double-NUL-terminated
// UTF-16LE with NO length prefix (MnidString), the FastTransfer form, distinct
// from the length-prefixed PROPERTY_NAME used elsewhere.
func appendName(b []byte, n mapi.PropertyName) ([]byte, error) {
	f := n.GUID.Flat()
	b = append(b, f[:]...)
	b = append(b, n.Kind)
	switch n.Kind {
	case mapi.MnidID:
		b = binary.LittleEndian.AppendUint32(b, n.LID)
	case mapi.MnidString:
		b = append(b, encodeUTF16(n.Name)...)
	case mapi.KindNone:
		// nothing follows the GUID
	default:
		return nil, fmt.Errorf("ics: invalid named-property kind %#x", n.Kind)
	}
	return b, nil
}

// decodeName reads an inline PROPERTY_NAME. ok is false on a short read.
func decodeName(b []byte) (mapi.PropertyName, int, bool) {
	r := reader{b: b}
	raw, ok := r.bytes(16)
	if !ok {
		return mapi.PropertyName{}, 0, false
	}
	var f mapi.FlatUID
	copy(f[:], raw)
	n := mapi.PropertyName{GUID: f.GUID()}
	kind, ok := r.byte()
	if !ok {
		return mapi.PropertyName{}, 0, false
	}
	n.Kind = kind
	switch kind {
	case mapi.MnidID:
		lid, ok := r.u32()
		if !ok {
			return mapi.PropertyName{}, 0, false
		}
		n.LID = lid
	case mapi.MnidString:
		name, c, ok := readNakedUTF16(b[r.pos:])
		if !ok {
			return mapi.PropertyName{}, 0, false
		}
		n.Name = name
		r.pos += c
	case mapi.KindNone:
		// nothing follows
	default:
		return mapi.PropertyName{}, 0, false
	}
	return n, r.pos, true
}

// readNakedUTF16 reads UTF-16LE code units up to and including a 0x0000
// terminator, returning the decoded string and bytes consumed. ok is false when
// no terminator is present yet (more bytes needed).
func readNakedUTF16(b []byte) (string, int, bool) {
	for i := 0; i+1 < len(b); i += 2 {
		if b[i] == 0 && b[i+1] == 0 {
			return decodeUTF16(b[:i+2]), i + 2, true
		}
	}
	return "", 0, false
}

// appendMV writes a multivalue: a u32 element count then each element in its
// scalar FastTransfer form.
func appendMV(b []byte, typ mapi.PropType, value any) ([]byte, error) {
	write, ok := mvWriters[typ]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errUnsupportedFXType, typ)
	}
	return write(b, value)
}

// mvWriter writes one multivalue's count and elements.
type mvWriter func(b []byte, value any) ([]byte, error)

// mvTyped adapts a per-element writer to the untyped table entry, refusing a
// value that is not the Go slice the property type calls for.
func mvTyped[T any](write func([]byte, T) []byte) mvWriter {
	return func(b []byte, value any) ([]byte, error) {
		xs, err := asVal[[]T](value)
		if err != nil {
			return nil, err
		}
		return appendMVElems(b, xs, write), nil
	}
}

// appendLenPrefixed writes one tearable element: its length, then its bytes.
func appendLenPrefixed(b, body []byte) []byte {
	// #nosec G115 -- a Go slice length; the buffer it measures is orders of magnitude below the field
	b = binary.LittleEndian.AppendUint32(b, uint32(len(body)))
	return append(b, body...)
}

// mvWriters is the multivalue vocabulary the FastTransfer encoder writes.
var mvWriters = map[mapi.PropType]mvWriter{
	mapi.PtMvShort: mvTyped(func(b []byte, x int16) []byte {
		return binary.LittleEndian.AppendUint16(b, uint16(x)) // #nosec G115 -- the signed and unsigned views of the same 16 bits
	}),
	mapi.PtMvLong: mvTyped(func(b []byte, x int32) []byte {
		return binary.LittleEndian.AppendUint32(b, uint32(x)) // #nosec G115 -- the signed and unsigned views of the same 32 bits
	}),
	mapi.PtMvCurrency: mvInt64,
	mapi.PtMvI8:       mvInt64,
	mapi.PtMvSysTime: mvTyped(func(b []byte, x uint64) []byte {
		return binary.LittleEndian.AppendUint64(b, x)
	}),
	mapi.PtMvFloat: mvTyped(func(b []byte, x float32) []byte {
		return binary.LittleEndian.AppendUint32(b, math.Float32bits(x))
	}),
	mapi.PtMvDouble:  mvDouble,
	mapi.PtMvAppTime: mvDouble,
	mapi.PtMvCLSID: mvTyped(func(b []byte, x mapi.GUID) []byte {
		f := x.Flat()
		return append(b, f[:]...)
	}),
	mapi.PtMvString8: mvTyped(func(b []byte, x string) []byte {
		return appendLenPrefixed(b, append([]byte(x), 0))
	}),
	mapi.PtMvUnicode: mvTyped(func(b []byte, x string) []byte {
		return appendLenPrefixed(b, encodeUTF16(x))
	}),
	mapi.PtMvBinary: mvTyped(appendLenPrefixed),
}

// mvInt64 and mvDouble back the multivalue types that share one encoding.
var (
	mvInt64 = mvTyped(func(b []byte, x int64) []byte {
		return binary.LittleEndian.AppendUint64(b, uint64(x)) // #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	})
	mvDouble = mvTyped(func(b []byte, x float64) []byte {
		return binary.LittleEndian.AppendUint64(b, math.Float64bits(x))
	})
)

func appendMVElems[T any](b []byte, xs []T, w func([]byte, T) []byte) []byte {
	// #nosec G115 -- a Go slice length; the buffer it measures is orders of magnitude below the field
	b = binary.LittleEndian.AppendUint32(b, uint32(len(xs)))
	for _, x := range xs {
		b = w(b, x)
	}
	return b
}

// decodeMV reads a multivalue written by appendMV.
func decodeMV(b []byte, typ mapi.PropType) (any, int, bool, error) {
	r := reader{b: b}
	count, ok := r.u32()
	if !ok {
		return nil, 0, false, nil
	}
	if count > maxMVCount {
		return nil, 0, false, fmt.Errorf("ics: implausible multivalue count %d", count)
	}
	read, ok := mvReaders[typ]
	if !ok {
		return nil, 0, false, fmt.Errorf("%w: %s", errUnsupportedFXType, typ)
	}
	return read(&r, count)
}

// mvReader reads one multivalue's elements off a cursor already past its count.
type mvReader func(r *reader, count uint32) (any, int, bool, error)

// mvTypedReader adapts a per-element reader to the untyped table entry.
func mvTypedReader[T any](read func(*reader) (T, bool)) mvReader {
	return func(r *reader, count uint32) (any, int, bool, error) {
		return decodeMVElems(r, count, read)
	}
}

// mvReaders is the multivalue vocabulary the FastTransfer decoder reads.
var mvReaders = map[mapi.PropType]mvReader{
	mapi.PtMvShort: mvTypedReader(func(r *reader) (int16, bool) {
		v, ok := r.u16()
		return int16(v), ok // #nosec G115 -- the signed and unsigned views of the same 16 bits
	}),
	mapi.PtMvLong: mvTypedReader(func(r *reader) (int32, bool) {
		v, ok := r.u32()
		return int32(v), ok // #nosec G115 -- the signed and unsigned views of the same 32 bits
	}),
	mapi.PtMvCurrency: mvReadInt64,
	mapi.PtMvI8:       mvReadInt64,
	mapi.PtMvSysTime:  mvTypedReader(func(r *reader) (uint64, bool) { return r.u64() }),
	mapi.PtMvFloat: mvTypedReader(func(r *reader) (float32, bool) {
		v, ok := r.u32()
		return math.Float32frombits(v), ok
	}),
	mapi.PtMvDouble:  mvReadDouble,
	mapi.PtMvAppTime: mvReadDouble,
	mapi.PtMvCLSID: mvTypedReader(func(r *reader) (mapi.GUID, bool) {
		raw, ok := r.bytes(16)
		if !ok {
			return mapi.GUID{}, false
		}
		var f mapi.FlatUID
		copy(f[:], raw)
		return f.GUID(), true
	}),
	mapi.PtMvString8: mvTypedReader(func(r *reader) (string, bool) {
		raw, ok := r.lenPrefixed()
		if !ok {
			return "", false
		}
		return string(trimNUL(raw)), true
	}),
	mapi.PtMvUnicode: mvTypedReader(func(r *reader) (string, bool) {
		raw, ok := r.lenPrefixed()
		if !ok {
			return "", false
		}
		return decodeUTF16(raw), true
	}),
	mapi.PtMvBinary: mvTypedReader(func(r *reader) ([]byte, bool) {
		raw, ok := r.lenPrefixed()
		if !ok {
			return nil, false
		}
		out := make([]byte, len(raw)) // always non-nil
		copy(out, raw)
		return out, true
	}),
}

// mvReadInt64 and mvReadDouble back the multivalue types that share one encoding.
var (
	mvReadInt64 = mvTypedReader(func(r *reader) (int64, bool) {
		v, ok := r.u64()
		return int64(v), ok // #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	})
	mvReadDouble = mvTypedReader(func(r *reader) (float64, bool) {
		v, ok := r.u64()
		return math.Float64frombits(v), ok
	})
)

func decodeMVElems[T any](r *reader, count uint32, rd func(*reader) (T, bool)) (any, int, bool, error) {
	// The absolute cap above still admits a count that the stream cannot possibly
	// satisfy, so check it against the bytes actually left before it becomes a make()
	// length. Every element costs at least two wire bytes, so a larger count is either
	// corruption or a request to reserve megabytes from a handful of bytes.
	if int64(count)*2 > int64(r.remaining()) {
		return nil, 0, false, fmt.Errorf("ics: multivalue count %d exceeds the %d bytes left", count, r.remaining())
	}
	xs := make([]T, count)
	for i := range xs {
		v, ok := rd(r)
		if !ok {
			return nil, 0, false, nil
		}
		xs[i] = v
	}
	return xs, r.pos, true, nil
}
