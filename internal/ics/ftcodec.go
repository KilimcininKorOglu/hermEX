package ics

import (
	"encoding/binary"
	"fmt"
	"math"
	"unicode/utf16"

	"hermex/internal/mapi"
)

// StreamProp is one property carried on the FastTransfer stream: its tag, the Go
// value (typed per the mapi value model, exactly as in ext.PropValue), and, for
// a named property (tag id >= 0x8000), its inline name. Resolving a named tag's
// store-local id against the named-property table is the caller's job
// (download/upload), not the codec's.
type StreamProp struct {
	Tag   mapi.PropTag
	Name  *mapi.PropertyName
	Value any
}

// Item is one decoded FastTransfer element: either a structural marker or a
// property value.
type Item struct {
	IsMarker bool
	Marker   uint32
	Prop     *StreamProp
}

// errUnsupportedFXType reports a property type that has no FastTransfer wire form
// in v1 (PT_RESTRICTION, PT_ACTIONS, PT_UNSPECIFIED). The caller must exclude
// such properties from the set it streams rather than corrupting the stream.
var errUnsupportedFXType = fmt.Errorf("ics: property type has no FastTransfer encoding")

// encodeProp serializes a property into an atomic header (the propdef, optional
// inline name, and for a variable-length value its u32 length prefix or the
// whole fixed/multivalue body) plus an optional tearable body (the raw bytes of
// a string or binary value). A chunk boundary may fall anywhere inside body but
// never inside header. PT_SVREID must be filtered by the caller (it has no
// stream form); other unsupported types return errUnsupportedFXType.
//
// Strings are written UTF-16LE (the FORCE_UNICODE convention real clients
// negotiate), except PR_MESSAGE_CLASS which is always PT_STRING8.
func encodeProp(p StreamProp) (header, body []byte, err error) {
	typ := p.Tag.Type()
	propid := p.Tag.ID()
	wireType := fxWireType(typ, propid)

	header = binary.LittleEndian.AppendUint32(header, uint32(propid)<<16|uint32(wireType))
	if propid >= 0x8000 {
		if p.Name == nil {
			return nil, nil, fmt.Errorf("ics: named property %s missing PropertyName", p.Tag)
		}
		if header, err = appendName(header, *p.Name); err != nil {
			return nil, nil, err
		}
	}

	if write, ok := fxValueWriters[wireType]; ok {
		return write(header, p.Value)
	}
	if isMultivalue(wireType) {
		if header, err = appendMV(header, wireType, p.Value); err != nil {
			return nil, nil, err
		}
		return header, nil, nil
	}
	return nil, nil, fmt.Errorf("%w: %s", errUnsupportedFXType, typ)
}

// fxWireType is the type a property is written as: strings go out UTF-16LE (the
// FORCE_UNICODE convention real clients negotiate), except PR_MESSAGE_CLASS which
// is always PT_STRING8. Every other type is written as itself.
func fxWireType(typ mapi.PropType, propid uint16) mapi.PropType {
	if typ != mapi.PtString8 && typ != mapi.PtUnicode {
		return typ
	}
	if propid == propIDMessageCls {
		return mapi.PtString8
	}
	return mapi.PtUnicode
}

// fxValueWriter appends one property value to the element header and returns it
// with the optional tearable body (the raw bytes of a string or binary value).
type fxValueWriter func(header []byte, v any) (hdr, body []byte, err error)

// fxTyped adapts a typed writer to the untyped table entry, refusing a value that
// is not the Go type the property type calls for.
func fxTyped[T any](write func(header []byte, x T) ([]byte, []byte, error)) fxValueWriter {
	return func(header []byte, v any) ([]byte, []byte, error) {
		x, err := asVal[T](v)
		if err != nil {
			return nil, nil, err
		}
		return write(header, x)
	}
}

// fxValueWriters is the fixed-type vocabulary the FastTransfer encoder writes. A
// type absent from it is either a multivalue or unsupported on this wire.
var fxValueWriters = map[mapi.PropType]fxValueWriter{
	mapi.PtShort: fxTyped(func(h []byte, x int16) ([]byte, []byte, error) {
		return binary.LittleEndian.AppendUint16(h, uint16(x)), nil, nil // #nosec G115 -- the signed and unsigned views of the same 16 bits
	}),
	mapi.PtLong: func(h []byte, v any) ([]byte, []byte, error) {
		h, err := appendU32Val(h, v, mapi.PtLong)
		return h, nil, err
	},
	mapi.PtError: func(h []byte, v any) ([]byte, []byte, error) {
		h, err := appendU32Val(h, v, mapi.PtError)
		return h, nil, err
	},
	mapi.PtFloat: fxTyped(func(h []byte, x float32) ([]byte, []byte, error) {
		return binary.LittleEndian.AppendUint32(h, math.Float32bits(x)), nil, nil
	}),
	mapi.PtDouble:  fxDouble,
	mapi.PtAppTime: fxDouble,
	mapi.PtBoolean: fxTyped(func(h []byte, x bool) ([]byte, []byte, error) {
		var b uint16
		if x {
			b = 1
		}
		return binary.LittleEndian.AppendUint16(h, b), nil, nil // PT_BOOLEAN is 2 bytes on the FX wire
	}),
	mapi.PtCurrency: fxInt64,
	mapi.PtI8:       fxInt64,
	mapi.PtSysTime: fxTyped(func(h []byte, x uint64) ([]byte, []byte, error) {
		return binary.LittleEndian.AppendUint64(h, x), nil, nil
	}),
	mapi.PtCLSID: fxTyped(func(h []byte, x mapi.GUID) ([]byte, []byte, error) {
		f := x.Flat()
		return append(h, f[:]...), nil, nil
	}),
	mapi.PtUnicode: fxTyped(func(h []byte, s string) ([]byte, []byte, error) {
		return appendTearable(h, encodeUTF16(s))
	}),
	mapi.PtString8: fxTyped(func(h []byte, s string) ([]byte, []byte, error) {
		return appendTearable(h, append([]byte(s), 0)) // code-page bytes + NUL; length includes the NUL
	}),
	mapi.PtBinary: fxBinary,
	mapi.PtObject: fxBinary,
}

// fxDouble, fxInt64 and fxBinary back the types that share one encoding.
var (
	fxDouble = fxTyped(func(h []byte, x float64) ([]byte, []byte, error) {
		return binary.LittleEndian.AppendUint64(h, math.Float64bits(x)), nil, nil
	})
	fxInt64 = fxTyped(func(h []byte, x int64) ([]byte, []byte, error) {
		return binary.LittleEndian.AppendUint64(h, uint64(x)), nil, nil // #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	})
	fxBinary = fxTyped(func(h []byte, b []byte) ([]byte, []byte, error) {
		return appendTearable(h, b)
	})
)

// appendTearable writes a tearable value: its length in the header, the bytes as
// the body a chunk boundary may fall inside.
func appendTearable(header, body []byte) ([]byte, []byte, error) {
	// #nosec G115 -- a Go slice length; the buffer it measures is orders of magnitude below the field
	return binary.LittleEndian.AppendUint32(header, uint32(len(body))), body, nil
}

// decodeElement reads one element from the front of b. complete is false (and
// consumed 0) when b holds only part of the element, the caller buffers more
// bytes and retries from the same offset. This length-driven rewind makes the
// reader tolerant of a chunk boundary falling anywhere, including mid-primitive.
func decodeElement(b []byte) (it Item, consumed int, complete bool, err error) {
	r := reader{b: b}
	word, ok := r.u32()
	if !ok {
		return Item{}, 0, false, nil
	}
	if isMarker(word) {
		return Item{IsMarker: true, Marker: word}, r.pos, true, nil
	}

	propid := uint16(word >> 16)
	valueType := mapi.PropType(word & 0xFFFF)
	tagType := valueType
	if word == metaTagIdsetGiven { // the type field lies; the body is binary
		valueType, tagType = mapi.PtBinary, mapi.PtBinary
	}

	var name *mapi.PropertyName
	if propid >= 0x8000 {
		n, c, ok := decodeName(b[r.pos:])
		if !ok {
			return Item{}, 0, false, nil
		}
		r.pos += c
		name = &n
	}

	if uint16(valueType)&fxCodepageFlag != 0 { // code-page string
		if uint16(valueType)&^fxCodepageFlag == cpUTF16 {
			valueType, tagType = mapi.PtUnicode, mapi.PtUnicode
		} else {
			valueType, tagType = mapi.PtString8, mapi.PtString8
		}
	}

	val, c, ok, err := decodeValue(b[r.pos:], valueType)
	if err != nil {
		return Item{}, 0, false, err
	}
	if !ok {
		return Item{}, 0, false, nil
	}
	r.pos += c
	tag := mapi.PropTag(uint32(propid)<<16 | uint32(tagType))
	return Item{Prop: &StreamProp{Tag: tag, Name: name, Value: val}}, r.pos, true, nil
}

// decodeValue reads a single value body of the given type. ok is false on a
// short read (incomplete), err is set only for a type with no stream form.
func decodeValue(b []byte, typ mapi.PropType) (val any, consumed int, ok bool, err error) {
	read, known := fxValueReaders[typ]
	if !known {
		if isMultivalue(typ) {
			return decodeMV(b, typ)
		}
		return nil, 0, false, fmt.Errorf("%w: %s", errUnsupportedFXType, typ)
	}
	r := reader{b: b}
	val, ok = read(&r)
	if !ok {
		return nil, 0, false, nil
	}
	return val, r.pos, true, nil
}

// fxValueReader reads one fixed-type value body off the cursor. ok is false on a
// short read, which the caller answers by buffering more bytes and retrying.
type fxValueReader func(r *reader) (any, bool)

// fxValueReaders is the fixed-type vocabulary the FastTransfer decoder reads. A
// type absent from it is either a multivalue or unsupported on this wire.
var fxValueReaders = map[mapi.PropType]fxValueReader{
	mapi.PtShort: func(r *reader) (any, bool) {
		v, ok := r.u16()
		return int16(v), ok // #nosec G115 -- the signed and unsigned views of the same 16 bits
	},
	mapi.PtLong: func(r *reader) (any, bool) {
		v, ok := r.u32()
		return int32(v), ok // #nosec G115 -- the signed and unsigned views of the same 32 bits
	},
	mapi.PtError: func(r *reader) (any, bool) { return r.u32() },
	mapi.PtFloat: func(r *reader) (any, bool) {
		v, ok := r.u32()
		return math.Float32frombits(v), ok
	},
	mapi.PtDouble:  fxReadDouble,
	mapi.PtAppTime: fxReadDouble,
	mapi.PtBoolean: func(r *reader) (any, bool) {
		v, ok := r.u16()
		return v != 0, ok
	},
	mapi.PtCurrency: fxReadInt64,
	mapi.PtI8:       fxReadInt64,
	mapi.PtSysTime:  func(r *reader) (any, bool) { return r.u64() },
	mapi.PtCLSID: func(r *reader) (any, bool) {
		raw, ok := r.bytes(16)
		if !ok {
			return nil, false
		}
		var f mapi.FlatUID
		copy(f[:], raw)
		return f.GUID(), true
	},
	mapi.PtUnicode: func(r *reader) (any, bool) {
		raw, ok := r.lenPrefixed()
		if !ok {
			return nil, false
		}
		return decodeUTF16(raw), true
	},
	mapi.PtString8: func(r *reader) (any, bool) {
		raw, ok := r.lenPrefixed()
		if !ok {
			return nil, false
		}
		return string(trimNUL(raw)), true
	},
	mapi.PtBinary: fxReadBinary,
	mapi.PtObject: fxReadBinary,
}

// fxReadDouble, fxReadInt64 and fxReadBinary back the types that share one
// encoding.
var (
	fxReadDouble fxValueReader = func(r *reader) (any, bool) {
		v, ok := r.u64()
		return math.Float64frombits(v), ok
	}
	fxReadInt64 fxValueReader = func(r *reader) (any, bool) {
		v, ok := r.u64()
		return int64(v), ok // #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	}
	fxReadBinary fxValueReader = func(r *reader) (any, bool) {
		raw, ok := r.lenPrefixed()
		if !ok {
			return nil, false
		}
		out := make([]byte, len(raw)) // always non-nil, even for a zero-length value
		copy(out, raw)
		return out, true
	}
)

// reader is a cursor over a byte slice; each read reports ok=false (without
// advancing) when fewer bytes remain than requested.
type reader struct {
	b   []byte
	pos int
}

// remaining reports how many unread bytes are left, so a decoded element count can
// be checked against them before it becomes an allocation length.
func (r *reader) remaining() int { return len(r.b) - r.pos }

func (r *reader) u16() (uint16, bool) {
	if r.pos+2 > len(r.b) {
		return 0, false
	}
	v := binary.LittleEndian.Uint16(r.b[r.pos:])
	r.pos += 2
	return v, true
}

func (r *reader) u32() (uint32, bool) {
	if r.pos+4 > len(r.b) {
		return 0, false
	}
	v := binary.LittleEndian.Uint32(r.b[r.pos:])
	r.pos += 4
	return v, true
}

func (r *reader) u64() (uint64, bool) {
	if r.pos+8 > len(r.b) {
		return 0, false
	}
	v := binary.LittleEndian.Uint64(r.b[r.pos:])
	r.pos += 8
	return v, true
}

func (r *reader) bytes(n int) ([]byte, bool) {
	if n < 0 || r.pos+n > len(r.b) {
		return nil, false
	}
	v := r.b[r.pos : r.pos+n]
	r.pos += n
	return v, true
}

// lenPrefixed reads a u32 byte count then that many bytes; ok is false if either
// the count or the body is short.
func (r *reader) lenPrefixed() ([]byte, bool) {
	n, ok := r.u32()
	if !ok {
		return nil, false
	}
	return r.bytes(int(n))
}

func asVal[T any](v any) (T, error) {
	t, ok := v.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("ics: value of type %T is not %T", v, zero)
	}
	return t, nil
}

// appendU32Val handles PT_LONG (Go int32) and PT_ERROR (Go uint32), which share
// the wire form but differ in Go type.
func appendU32Val(b []byte, v any, typ mapi.PropType) ([]byte, error) {
	if typ == mapi.PtError {
		x, err := asVal[uint32](v)
		if err != nil {
			return nil, err
		}
		return binary.LittleEndian.AppendUint32(b, x), nil
	}
	x, err := asVal[int32](v)
	if err != nil {
		return nil, err
	}
	// #nosec G115 -- the signed and unsigned views of the same 32 bits
	return binary.LittleEndian.AppendUint32(b, uint32(x)), nil
}

func isMultivalue(t mapi.PropType) bool { return t&mapi.MvFlag == mapi.MvFlag }

func encodeUTF16(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2+2)
	for _, c := range u {
		b = binary.LittleEndian.AppendUint16(b, c)
	}
	return binary.LittleEndian.AppendUint16(b, 0) // double-byte NUL terminator
}

func decodeUTF16(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(b[i:]))
	}
	for len(u) > 0 && u[len(u)-1] == 0 { // strip the terminator
		u = u[:len(u)-1]
	}
	return string(utf16.Decode(u))
}

func trimNUL(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == 0 {
		return b[:len(b)-1]
	}
	return b
}
