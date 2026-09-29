package tnef

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"unicode/utf16"

	"hermex/internal/mapi"
)

// Encode writes a TNEF stream ([MS-OXTNEF]) that carries a message's properties
// and its attachments. The class also travels as attMessageClass, which a reader
// that predates the property lists reads. Every string is written as PtUnicode, so
// the stream needs no code page. A recipient table is not written: the MIME
// headers carry the recipients of an ordinary message.
func Encode(m *Message) ([]byte, error) {
	var e encoder
	e.u32(signature)
	e.u16(0)
	e.attribute(levelMessage, attTnefVersion, binary.LittleEndian.AppendUint32(nil, tnefVersion))
	e.attribute(levelMessage, attOemCodepage, binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, 65001), 0))
	for _, p := range m.Props {
		if class, ok := p.Value.(string); ok && p.Tag.ID() == mapi.PrMessageClass.ID() {
			e.attribute(levelMessage, attMessageClass, append([]byte(class), 0))
		}
	}
	props, err := encodePropList(m.Props)
	if err != nil {
		return nil, err
	}
	e.attribute(levelMessage, attMsgProps, props)
	for i, a := range m.Attachments {
		list, err := encodePropList(a.Props)
		if err != nil {
			return nil, fmt.Errorf("attachment %d: %w", i, err)
		}
		e.attribute(levelAttachment, attAttachRendData, fileRendData())
		e.attribute(levelAttachment, attAttachment, list)
	}
	return e.b.Bytes(), nil
}

// fileRendData is the attAttachRendData of a file attachment whose rendering
// position is not set ([MS-OXTNEF] 2.3.3.9).
func fileRendData() []byte {
	out := []byte{1, 0}
	out = binary.LittleEndian.AppendUint32(out, 0xFFFFFFFF)
	return append(out, make([]byte, 8)...)
}

// encoder accumulates little-endian output.
type encoder struct{ b bytes.Buffer }

func (e *encoder) u16(v uint16) { e.b.Write(binary.LittleEndian.AppendUint16(nil, v)) }
func (e *encoder) u32(v uint32) { e.b.Write(binary.LittleEndian.AppendUint32(nil, v)) }

// attribute writes one attribute with its length and checksum.
func (e *encoder) attribute(level byte, id uint32, data []byte) {
	e.b.WriteByte(level)
	e.u32(id)
	e.u32(uint32(len(data))) // #nosec G115 -- a property list is far below 4 GiB
	e.b.Write(data)
	e.u16(checksum(data))
}

// pad writes the zero bytes that bring a value of n bytes to a 4-byte boundary.
func (e *encoder) pad(n int) { e.b.Write(make([]byte, (4-n%4)%4)) }

// variable writes one length-prefixed, padded value.
func (e *encoder) variable(v []byte) {
	e.u32(uint32(len(v))) // #nosec G115 -- a value is far below 4 GiB
	e.b.Write(v)
	e.pad(len(v))
}

// encodePropList writes a counted property list (MsgPropertyList). A PtObject
// property is left out: its value is an interface the stream cannot restate.
func encodePropList(props []Prop) ([]byte, error) {
	var body encoder
	n := uint32(0)
	for _, p := range props {
		if p.Tag.Type().Base() == mapi.PtObject {
			continue
		}
		if err := body.prop(p); err != nil {
			return nil, err
		}
		n++
	}
	return append(binary.LittleEndian.AppendUint32(nil, n), body.b.Bytes()...), nil
}

// prop writes one property: its tag, the name of a named property, and the value.
// A PtString8 value is written as PtUnicode.
func (e *encoder) prop(p Prop) error {
	typ := p.Tag.Type()
	if typ.Base() == mapi.PtString8 {
		typ = typ&mapi.MvFlag | mapi.PtUnicode
	}
	if p.Tag.ID() >= 0x8000 && p.Name == nil {
		return fmt.Errorf("tnef: named property %#x has no name", p.Tag.ID())
	}
	e.u16(uint16(typ))
	e.u16(p.Tag.ID())
	if p.Name != nil {
		e.name(*p.Name)
	}
	if typ.IsMultivalue() {
		return e.multiValue(p.Tag, typ.Base(), p.Value)
	}
	if isVariable(typ) {
		e.u32(1)
	}
	return e.value(p.Tag, typ, p.Value)
}

// name writes a NamedPropSpec.
func (e *encoder) name(n mapi.PropertyName) {
	e.b.Write(guidBytes(n.GUID))
	if n.Kind == mapi.MnidID {
		e.u32(0)
		e.u32(n.LID)
		return
	}
	e.u32(1)
	e.variable(utf16z(n.Name))
}

// multiValue writes a counted run of values.
func (e *encoder) multiValue(tag mapi.PropTag, base mapi.PropType, v any) error {
	vals, ok := sliceValues(v)
	if !ok {
		return fmt.Errorf("tnef: property %#x holds %T, not a list", uint32(tag), v)
	}
	e.u32(uint32(len(vals))) // #nosec G115 -- a list is far below 4 GiB
	for _, one := range vals {
		if err := e.value(tag, base, one); err != nil {
			return err
		}
	}
	return nil
}

// sliceValues spreads a multivalue slice into its elements.
func sliceValues(v any) ([]any, bool) {
	switch s := v.(type) {
	case []int16:
		return spread(s), true
	case []int32:
		return spread(s), true
	case []float32:
		return spread(s), true
	case []float64:
		return spread(s), true
	case []int64:
		return spread(s), true
	case []uint64:
		return spread(s), true
	case []mapi.GUID:
		return spread(s), true
	case []string:
		return spread(s), true
	case [][]byte:
		return spread(s), true
	}
	return nil, false
}

func spread[T any](s []T) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// value writes one value of a scalar type t, padded to a 4-byte boundary.
func (e *encoder) value(tag mapi.PropTag, t mapi.PropType, v any) error {
	if t == mapi.PtNull || t == mapi.PtUnspecified {
		return nil
	}
	raw, ok := valueBytes(t, v)
	if !ok {
		return fmt.Errorf("tnef: property %#x holds %T", uint32(tag), v)
	}
	if isVariable(t) {
		e.variable(raw)
		return nil
	}
	e.b.Write(raw)
	e.pad(len(raw))
	return nil
}

// valueBytes encodes one value; false when the Go value does not fit the type.
func valueBytes(t mapi.PropType, v any) ([]byte, bool) {
	switch t {
	case mapi.PtUnicode, mapi.PtString8:
		s, ok := v.(string)
		return utf16z(s), ok
	case mapi.PtBinary:
		b, ok := v.([]byte)
		return b, ok
	case mapi.PtCLSID:
		g, ok := v.(mapi.GUID)
		return guidBytes(g), ok
	case mapi.PtBoolean:
		b, ok := v.(bool)
		return boolBytes(b), ok
	}
	return numberBytes(t, v)
}

// numberBytes encodes a numeric value of fixed width.
func numberBytes(t mapi.PropType, v any) ([]byte, bool) {
	le := binary.LittleEndian
	switch x := v.(type) {
	case int16:
		return le.AppendUint16(nil, uint16(x)), t == mapi.PtShort // #nosec G115 -- a bit-for-bit reinterpretation
	case int32:
		return le.AppendUint32(nil, uint32(x)), t == mapi.PtLong // #nosec G115 -- a bit-for-bit reinterpretation
	case uint32:
		return le.AppendUint32(nil, x), t == mapi.PtError
	case float32:
		return le.AppendUint32(nil, math.Float32bits(x)), t == mapi.PtFloat
	case float64:
		return le.AppendUint64(nil, math.Float64bits(x)), t == mapi.PtDouble || t == mapi.PtAppTime
	case int64:
		return le.AppendUint64(nil, uint64(x)), t == mapi.PtI8 || t == mapi.PtCurrency // #nosec G115 -- a bit-for-bit reinterpretation
	case uint64:
		return le.AppendUint64(nil, x), t == mapi.PtSysTime
	}
	return nil, false
}

// boolBytes encodes a PtBoolean, whose value is two bytes in a stream.
func boolBytes(b bool) []byte {
	if b {
		return []byte{1, 0}
	}
	return []byte{0, 0}
}

// guidBytes writes a GUID in its wire order.
func guidBytes(g mapi.GUID) []byte {
	out := binary.LittleEndian.AppendUint32(nil, g.Data1)
	out = binary.LittleEndian.AppendUint16(out, g.Data2)
	out = binary.LittleEndian.AppendUint16(out, g.Data3)
	return append(out, g.Data4[:]...)
}

// utf16z encodes text as null-terminated UTF-16LE.
func utf16z(s string) []byte {
	var out []byte
	for _, c := range utf16.Encode([]rune(s)) {
		out = binary.LittleEndian.AppendUint16(out, c)
	}
	return append(out, 0, 0)
}
