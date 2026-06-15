package ics

import (
	"encoding/binary"
	"fmt"
	"math"
	"unicode/utf16"

	"hermex/internal/mapi"
)

// StreamProp is one property carried on the FastTransfer stream: its tag, the Go
// value (typed per the mapi value model, exactly as in ext.PropValue), and — for
// a named property (tag id >= 0x8000) — its inline name. Resolving a named tag's
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
	wireType := typ
	if typ == mapi.PtString8 || typ == mapi.PtUnicode {
		if propid == propIDMessageCls {
			wireType = mapi.PtString8
		} else {
			wireType = mapi.PtUnicode
		}
	}

	header = binary.LittleEndian.AppendUint32(header, uint32(propid)<<16|uint32(wireType))
	if propid >= 0x8000 {
		if p.Name == nil {
			return nil, nil, fmt.Errorf("ics: named property %s missing PropertyName", p.Tag)
		}
		header, err = appendName(header, *p.Name)
		if err != nil {
			return nil, nil, err
		}
	}

	switch wireType {
	case mapi.PtShort:
		v, err := asVal[int16](p.Value)
		if err != nil {
			return nil, nil, err
		}
		header = binary.LittleEndian.AppendUint16(header, uint16(v))
	case mapi.PtLong, mapi.PtError:
		header, err = appendU32Val(header, p.Value, typ)
		if err != nil {
			return nil, nil, err
		}
	case mapi.PtFloat:
		v, err := asVal[float32](p.Value)
		if err != nil {
			return nil, nil, err
		}
		header = binary.LittleEndian.AppendUint32(header, math.Float32bits(v))
	case mapi.PtDouble, mapi.PtAppTime:
		v, err := asVal[float64](p.Value)
		if err != nil {
			return nil, nil, err
		}
		header = binary.LittleEndian.AppendUint64(header, math.Float64bits(v))
	case mapi.PtBoolean:
		v, err := asVal[bool](p.Value)
		if err != nil {
			return nil, nil, err
		}
		var b uint16
		if v {
			b = 1
		}
		header = binary.LittleEndian.AppendUint16(header, b) // PT_BOOLEAN is 2 bytes on the FX wire
	case mapi.PtCurrency, mapi.PtI8:
		v, err := asVal[int64](p.Value)
		if err != nil {
			return nil, nil, err
		}
		header = binary.LittleEndian.AppendUint64(header, uint64(v))
	case mapi.PtSysTime:
		v, err := asVal[uint64](p.Value)
		if err != nil {
			return nil, nil, err
		}
		header = binary.LittleEndian.AppendUint64(header, v)
	case mapi.PtCLSID:
		v, err := asVal[mapi.GUID](p.Value)
		if err != nil {
			return nil, nil, err
		}
		f := v.Flat()
		header = append(header, f[:]...)
	case mapi.PtUnicode:
		s, err := asVal[string](p.Value)
		if err != nil {
			return nil, nil, err
		}
		body = encodeUTF16(s)
		header = binary.LittleEndian.AppendUint32(header, uint32(len(body)))
	case mapi.PtString8:
		s, err := asVal[string](p.Value)
		if err != nil {
			return nil, nil, err
		}
		body = append([]byte(s), 0) // code-page bytes + NUL; length includes the NUL
		header = binary.LittleEndian.AppendUint32(header, uint32(len(body)))
	case mapi.PtBinary, mapi.PtObject:
		b, err := asVal[[]byte](p.Value)
		if err != nil {
			return nil, nil, err
		}
		body = b
		header = binary.LittleEndian.AppendUint32(header, uint32(len(b)))
	default:
		if isMultivalue(wireType) {
			header, err = appendMV(header, wireType, p.Value)
			if err != nil {
				return nil, nil, err
			}
			return header, nil, nil
		}
		return nil, nil, fmt.Errorf("%w: %s", errUnsupportedFXType, typ)
	}
	return header, body, nil
}

// decodeElement reads one element from the front of b. complete is false (and
// consumed 0) when b holds only part of the element — the caller buffers more
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
	r := reader{b: b}
	switch typ {
	case mapi.PtShort:
		v, ok := r.u16()
		return int16(v), r.pos, ok, nil
	case mapi.PtLong:
		v, ok := r.u32()
		return int32(v), r.pos, ok, nil
	case mapi.PtError:
		v, ok := r.u32()
		return v, r.pos, ok, nil
	case mapi.PtFloat:
		v, ok := r.u32()
		return math.Float32frombits(v), r.pos, ok, nil
	case mapi.PtDouble, mapi.PtAppTime:
		v, ok := r.u64()
		return math.Float64frombits(v), r.pos, ok, nil
	case mapi.PtBoolean:
		v, ok := r.u16()
		return v != 0, r.pos, ok, nil
	case mapi.PtCurrency, mapi.PtI8:
		v, ok := r.u64()
		return int64(v), r.pos, ok, nil
	case mapi.PtSysTime:
		v, ok := r.u64()
		return v, r.pos, ok, nil
	case mapi.PtCLSID:
		raw, ok := r.bytes(16)
		if !ok {
			return nil, 0, false, nil
		}
		var f mapi.FlatUID
		copy(f[:], raw)
		return f.GUID(), r.pos, true, nil
	case mapi.PtUnicode:
		raw, ok := r.lenPrefixed()
		if !ok {
			return nil, 0, false, nil
		}
		return decodeUTF16(raw), r.pos, true, nil
	case mapi.PtString8:
		raw, ok := r.lenPrefixed()
		if !ok {
			return nil, 0, false, nil
		}
		return string(trimNUL(raw)), r.pos, true, nil
	case mapi.PtBinary, mapi.PtObject:
		raw, ok := r.lenPrefixed()
		if !ok {
			return nil, 0, false, nil
		}
		out := make([]byte, len(raw)) // always non-nil, even for a zero-length value
		copy(out, raw)
		return out, r.pos, true, nil
	default:
		if isMultivalue(typ) {
			return decodeMV(b, typ)
		}
		return nil, 0, false, fmt.Errorf("%w: %s", errUnsupportedFXType, typ)
	}
}

// reader is a cursor over a byte slice; each read reports ok=false (without
// advancing) when fewer bytes remain than requested.
type reader struct {
	b   []byte
	pos int
}

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
