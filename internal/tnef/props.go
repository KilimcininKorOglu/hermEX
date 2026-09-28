package tnef

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"unicode/utf16"

	"hermex/internal/mapi"
)

// Prop is one encapsulated property. Tag carries the type and, for an ordinary
// property, the id; a named property's id is the writer's own and meaningless to
// the reader, so Name identifies it and the reader maps it to a local id.
type Prop struct {
	Tag   mapi.PropTag
	Name  *mapi.PropertyName
	Value any
}

// Object is a PtObject value: the interface id and the data after it.
type Object struct {
	IID  mapi.GUID
	Data []byte
}

// IIDMessage is the interface id of an attached Message object, whose data is a
// complete TNEF stream of its own ([MS-OXTNEF] 2.1.3.4).
var IIDMessage = mapi.GUID{Data1: 0x00020307, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}

// errPropType reports a property type the encoding does not define.
var errPropType = errors.New("tnef: unknown property type")

// fixedSize is the encoded size of each scalar type of fixed width, before the
// padding to a 4-byte boundary.
var fixedSize = map[mapi.PropType]int{
	mapi.PtShort: 2, mapi.PtLong: 4, mapi.PtFloat: 4, mapi.PtDouble: 8,
	mapi.PtCurrency: 8, mapi.PtAppTime: 8, mapi.PtError: 4, mapi.PtBoolean: 2,
	mapi.PtI8: 8, mapi.PtSysTime: 8, mapi.PtCLSID: 16,
}

// isVariable reports whether a type is length-prefixed.
func isVariable(t mapi.PropType) bool {
	switch t {
	case mapi.PtString8, mapi.PtUnicode, mapi.PtBinary, mapi.PtObject:
		return true
	}
	return false
}

// propList decodes a counted property list (MsgPropertyList).
func (d *decoder) propList(r *reader) ([]Prop, error) {
	n, err := r.count(4)
	if err != nil {
		return nil, err
	}
	props := make([]Prop, 0, n)
	for range n {
		p, err := d.prop(r)
		if err != nil {
			return nil, err
		}
		props = append(props, p)
	}
	return props, nil
}

// prop decodes one property: its tag, its name when it is a named property, and
// its value.
func (d *decoder) prop(r *reader) (Prop, error) {
	typ, err := r.u16()
	if err != nil {
		return Prop{}, err
	}
	id, err := r.u16()
	if err != nil {
		return Prop{}, err
	}
	p := Prop{Tag: mapi.MakeTag(id, mapi.PropType(typ))}
	if id >= 0x8000 {
		name, err := propName(r)
		if err != nil {
			return Prop{}, err
		}
		p.Name = &name
	}
	p.Value, err = d.value(r, mapi.PropType(typ))
	return p, err
}

// propName decodes a NamedPropSpec: the namespace, then a number or a string.
func propName(r *reader) (mapi.PropertyName, error) {
	g, err := r.bytes(16)
	if err != nil {
		return mapi.PropertyName{}, err
	}
	name := mapi.PropertyName{GUID: guidOf(g)}
	kind, err := r.u32()
	if err != nil {
		return name, err
	}
	switch kind {
	case 0:
		name.Kind = mapi.MnidID
		name.LID, err = r.u32()
		return name, err
	case 1:
		name.Kind = mapi.MnidString
		n, err := r.count(1)
		if err != nil {
			return name, err
		}
		raw, err := r.bytes(n)
		if err != nil {
			return name, err
		}
		name.Name = utf16String(raw)
		return name, r.skipPad(n)
	}
	return name, fmt.Errorf("tnef: named property kind %d", kind)
}

// value decodes the value of one property of type t.
func (d *decoder) value(r *reader, t mapi.PropType) (any, error) {
	base := t.Base()
	if t.IsMultivalue() {
		return d.multiValue(r, base)
	}
	if isVariable(base) {
		n, err := r.count(4)
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, fmt.Errorf("tnef: %d values of a single-valued property", n)
		}
		return d.variable(r, base)
	}
	if base == mapi.PtNull || base == mapi.PtUnspecified {
		return nil, nil
	}
	return d.scalar(r, base)
}

// multiValue decodes a counted run of values of one scalar type.
func (d *decoder) multiValue(r *reader, base mapi.PropType) (any, error) {
	minSize := 4
	if size, ok := fixedSize[base]; ok {
		minSize = max(size, 4)
	}
	n, err := r.count(minSize)
	if err != nil {
		return nil, err
	}
	vals := make([]any, 0, n)
	for range n {
		var v any
		if isVariable(base) {
			v, err = d.variable(r, base)
		} else {
			v, err = d.scalar(r, base)
		}
		if err != nil {
			return nil, err
		}
		vals = append(vals, v)
	}
	return typedSlice(base, vals), nil
}

// variable decodes one length-prefixed value.
func (d *decoder) variable(r *reader, base mapi.PropType) (any, error) {
	n, err := r.count(1)
	if err != nil {
		return nil, err
	}
	raw, err := r.bytes(n)
	if err != nil {
		return nil, err
	}
	if err := r.skipPad(n); err != nil {
		return nil, err
	}
	switch base {
	case mapi.PtUnicode:
		return utf16String(raw), nil
	case mapi.PtString8:
		return d.ansi(trimNul(raw)), nil
	case mapi.PtObject:
		if len(raw) < 16 {
			return nil, errShort
		}
		return Object{IID: guidOf(raw[:16]), Data: raw[16:]}, nil
	}
	return raw, nil
}

// scalar decodes one value of fixed width and skips its padding.
func (d *decoder) scalar(r *reader, base mapi.PropType) (any, error) {
	size, ok := fixedSize[base]
	if !ok {
		return nil, errPropType
	}
	raw, err := r.bytes(size)
	if err != nil {
		return nil, err
	}
	if err := r.skipPad(size); err != nil {
		return nil, err
	}
	return scalarValue(base, raw), nil
}

// scalarValue converts the bytes of a fixed-width value to the Go type the
// property type carries in package mapi.
func scalarValue(base mapi.PropType, raw []byte) any {
	le := binary.LittleEndian
	switch base {
	case mapi.PtShort:
		return int16(le.Uint16(raw))
	case mapi.PtLong:
		return int32(le.Uint32(raw))
	case mapi.PtError:
		return le.Uint32(raw)
	case mapi.PtFloat:
		return math.Float32frombits(le.Uint32(raw))
	case mapi.PtDouble, mapi.PtAppTime:
		return math.Float64frombits(le.Uint64(raw))
	case mapi.PtCurrency, mapi.PtI8:
		return int64(le.Uint64(raw))
	case mapi.PtBoolean:
		return le.Uint16(raw) != 0
	case mapi.PtSysTime:
		return le.Uint64(raw)
	}
	return guidOf(raw)
}

// typedSlice turns decoded values into the slice type package mapi uses for the
// multivalue form of base.
func typedSlice(base mapi.PropType, vals []any) any {
	switch base {
	case mapi.PtShort:
		return collect[int16](vals)
	case mapi.PtLong:
		return collect[int32](vals)
	case mapi.PtFloat:
		return collect[float32](vals)
	case mapi.PtDouble, mapi.PtAppTime:
		return collect[float64](vals)
	case mapi.PtCurrency, mapi.PtI8:
		return collect[int64](vals)
	case mapi.PtSysTime:
		return collect[uint64](vals)
	case mapi.PtCLSID:
		return collect[mapi.GUID](vals)
	case mapi.PtString8, mapi.PtUnicode:
		return collect[string](vals)
	}
	return collect[[]byte](vals)
}

// collect converts a slice of values that all hold T.
func collect[T any](vals []any) []T {
	out := make([]T, 0, len(vals))
	for _, v := range vals {
		if t, ok := v.(T); ok {
			out = append(out, t)
		}
	}
	return out
}

// guidOf reads a GUID in its wire order (three little-endian fields, then eight
// bytes as they stand).
func guidOf(b []byte) mapi.GUID {
	le := binary.LittleEndian
	g := mapi.GUID{Data1: le.Uint32(b), Data2: le.Uint16(b[4:]), Data3: le.Uint16(b[6:])}
	copy(g.Data4[:], b[8:16])
	return g
}

// utf16String decodes UTF-16LE text, dropping the terminating null and anything
// after it.
func utf16String(raw []byte) string {
	u := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		c := binary.LittleEndian.Uint16(raw[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

// trimNul drops a terminating null and anything after it.
func trimNul(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}
