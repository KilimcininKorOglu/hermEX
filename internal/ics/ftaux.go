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
// UTF-16LE with NO length prefix (MnidString) — the FastTransfer form, distinct
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
	switch typ {
	case mapi.PtMvShort:
		xs, err := asVal[[]int16](value)
		if err != nil {
			return nil, err
		}
		return appendMVElems(b, xs, func(b []byte, x int16) []byte { return binary.LittleEndian.AppendUint16(b, uint16(x)) }), nil
	case mapi.PtMvLong:
		xs, err := asVal[[]int32](value)
		if err != nil {
			return nil, err
		}
		return appendMVElems(b, xs, func(b []byte, x int32) []byte { return binary.LittleEndian.AppendUint32(b, uint32(x)) }), nil
	case mapi.PtMvCurrency, mapi.PtMvI8:
		xs, err := asVal[[]int64](value)
		if err != nil {
			return nil, err
		}
		return appendMVElems(b, xs, func(b []byte, x int64) []byte { return binary.LittleEndian.AppendUint64(b, uint64(x)) }), nil
	case mapi.PtMvSysTime:
		xs, err := asVal[[]uint64](value)
		if err != nil {
			return nil, err
		}
		return appendMVElems(b, xs, func(b []byte, x uint64) []byte { return binary.LittleEndian.AppendUint64(b, x) }), nil
	case mapi.PtMvFloat:
		xs, err := asVal[[]float32](value)
		if err != nil {
			return nil, err
		}
		return appendMVElems(b, xs, func(b []byte, x float32) []byte { return binary.LittleEndian.AppendUint32(b, math.Float32bits(x)) }), nil
	case mapi.PtMvDouble, mapi.PtMvAppTime:
		xs, err := asVal[[]float64](value)
		if err != nil {
			return nil, err
		}
		return appendMVElems(b, xs, func(b []byte, x float64) []byte { return binary.LittleEndian.AppendUint64(b, math.Float64bits(x)) }), nil
	case mapi.PtMvCLSID:
		xs, err := asVal[[]mapi.GUID](value)
		if err != nil {
			return nil, err
		}
		return appendMVElems(b, xs, func(b []byte, x mapi.GUID) []byte { f := x.Flat(); return append(b, f[:]...) }), nil
	case mapi.PtMvString8:
		xs, err := asVal[[]string](value)
		if err != nil {
			return nil, err
		}
		return appendMVElems(b, xs, func(b []byte, x string) []byte {
			body := append([]byte(x), 0)
			b = binary.LittleEndian.AppendUint32(b, uint32(len(body)))
			return append(b, body...)
		}), nil
	case mapi.PtMvUnicode:
		xs, err := asVal[[]string](value)
		if err != nil {
			return nil, err
		}
		return appendMVElems(b, xs, func(b []byte, x string) []byte {
			body := encodeUTF16(x)
			b = binary.LittleEndian.AppendUint32(b, uint32(len(body)))
			return append(b, body...)
		}), nil
	case mapi.PtMvBinary:
		xs, err := asVal[[][]byte](value)
		if err != nil {
			return nil, err
		}
		return appendMVElems(b, xs, func(b []byte, x []byte) []byte {
			b = binary.LittleEndian.AppendUint32(b, uint32(len(x)))
			return append(b, x...)
		}), nil
	default:
		return nil, fmt.Errorf("%w: %s", errUnsupportedFXType, typ)
	}
}

func appendMVElems[T any](b []byte, xs []T, w func([]byte, T) []byte) []byte {
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
	switch typ {
	case mapi.PtMvShort:
		return decodeMVElems(&r, count, func(r *reader) (int16, bool) { v, ok := r.u16(); return int16(v), ok })
	case mapi.PtMvLong:
		return decodeMVElems(&r, count, func(r *reader) (int32, bool) { v, ok := r.u32(); return int32(v), ok })
	case mapi.PtMvCurrency, mapi.PtMvI8:
		return decodeMVElems(&r, count, func(r *reader) (int64, bool) { v, ok := r.u64(); return int64(v), ok })
	case mapi.PtMvSysTime:
		return decodeMVElems(&r, count, func(r *reader) (uint64, bool) { return r.u64() })
	case mapi.PtMvFloat:
		return decodeMVElems(&r, count, func(r *reader) (float32, bool) { v, ok := r.u32(); return math.Float32frombits(v), ok })
	case mapi.PtMvDouble, mapi.PtMvAppTime:
		return decodeMVElems(&r, count, func(r *reader) (float64, bool) { v, ok := r.u64(); return math.Float64frombits(v), ok })
	case mapi.PtMvCLSID:
		return decodeMVElems(&r, count, func(r *reader) (mapi.GUID, bool) {
			raw, ok := r.bytes(16)
			if !ok {
				return mapi.GUID{}, false
			}
			var f mapi.FlatUID
			copy(f[:], raw)
			return f.GUID(), true
		})
	case mapi.PtMvString8:
		return decodeMVElems(&r, count, func(r *reader) (string, bool) {
			raw, ok := r.lenPrefixed()
			if !ok {
				return "", false
			}
			return string(trimNUL(raw)), true
		})
	case mapi.PtMvUnicode:
		return decodeMVElems(&r, count, func(r *reader) (string, bool) {
			raw, ok := r.lenPrefixed()
			if !ok {
				return "", false
			}
			return decodeUTF16(raw), true
		})
	case mapi.PtMvBinary:
		return decodeMVElems(&r, count, func(r *reader) ([]byte, bool) {
			raw, ok := r.lenPrefixed()
			if !ok {
				return nil, false
			}
			out := make([]byte, len(raw)) // always non-nil
			copy(out, raw)
			return out, true
		})
	default:
		return nil, 0, false, fmt.Errorf("%w: %s", errUnsupportedFXType, typ)
	}
}

func decodeMVElems[T any](r *reader, count uint32, rd func(*reader) (T, bool)) (any, int, bool, error) {
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
