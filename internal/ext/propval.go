package ext

import (
	"fmt"

	"hermex/internal/mapi"
)

// asType asserts that v holds a value of type T, returning an ErrFormat-wrapped
// error otherwise. It guards the any-typed property values against a caller
// supplying the wrong Go type for a property type.
func asType[T any](v any) (T, error) {
	t, ok := v.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("%w: value of type %T is not %T", ErrFormat, v, zero)
	}
	return t, nil
}

// pushMV writes a multivalue property: a uint32 element count followed by each
// element. The uint32 count width is fixed for every PT_MV_* type.
func pushMV[T any](p *Push, v any, write func(*Push, T) error) error {
	xs, err := asType[[]T](v)
	if err != nil {
		return err
	}
	p.Uint32(uint32(len(xs)))
	for _, x := range xs {
		if err := write(p, x); err != nil {
			return err
		}
	}
	return nil
}

// pullMV reads a multivalue property written by pushMV.
func pullMV[T any](p *Pull, read func(*Pull) (T, error)) (any, error) {
	n, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	xs := make([]T, n)
	for i := range xs {
		if xs[i], err = read(p); err != nil {
			return nil, err
		}
	}
	return xs, nil
}

// abkGated reports whether the address-book value-present prefix applies to typ:
// the reference gates strings, binaries, and any multivalue.
func abkGated(typ mapi.PropType) bool {
	return typ == mapi.PtString8 || typ == mapi.PtUnicode || typ == mapi.PtBinary || typ.IsMultivalue()
}

// PropValue writes a bare (untyped) property value of the given type. The Go
// type expected in v is documented on mapi.TaggedPropVal.
func (p *Push) PropValue(typ mapi.PropType, v any) error {
	if p.flags&FlagABK != 0 && abkGated(typ) {
		// Address-book mode prefixes a value-present byte; a nil value is absent.
		if v == nil {
			p.Uint8(0)
			return nil
		}
		p.Uint8(0xFF)
	} else if typ&mapi.MviFlag == mapi.MviFlag {
		typ &^= mapi.MviFlag // a multivalue instance is written as a single value
	}
	switch typ {
	case mapi.PtNull:
		// Deliberate deviation: the reference g_propval has no PT_NULL case and
		// rejects it (bad_switch). PtypNull means "property present, no value",
		// so we encode it as the empty payload it denotes rather than erroring.
		return nil
	case mapi.PtUnspecified:
		x, err := asType[mapi.TypedPropVal](v)
		if err != nil {
			return err
		}
		return p.TypedPropVal(x)
	case mapi.PtSvrEID:
		x, err := asType[mapi.SVREID](v)
		if err != nil {
			return err
		}
		return p.SVREID(x)
	case mapi.PtRestriction:
		x, err := asType[mapi.Restriction](v)
		if err != nil {
			return err
		}
		return p.Restriction(x)
	case mapi.PtActions:
		x, err := asType[mapi.RuleActions](v)
		if err != nil {
			return err
		}
		return p.RuleActions(x)
	case mapi.PtShort:
		x, err := asType[int16](v)
		if err != nil {
			return err
		}
		p.Uint16(uint16(x))
	case mapi.PtLong:
		x, err := asType[int32](v)
		if err != nil {
			return err
		}
		p.Uint32(uint32(x))
	case mapi.PtError:
		x, err := asType[uint32](v)
		if err != nil {
			return err
		}
		p.Uint32(x)
	case mapi.PtFloat:
		x, err := asType[float32](v)
		if err != nil {
			return err
		}
		p.Float32(x)
	case mapi.PtDouble, mapi.PtAppTime:
		x, err := asType[float64](v)
		if err != nil {
			return err
		}
		p.Float64(x)
	case mapi.PtCurrency, mapi.PtI8:
		x, err := asType[int64](v)
		if err != nil {
			return err
		}
		p.Uint64(uint64(x))
	case mapi.PtSysTime:
		x, err := asType[uint64](v)
		if err != nil {
			return err
		}
		p.Uint64(x)
	case mapi.PtBoolean:
		x, err := asType[bool](v)
		if err != nil {
			return err
		}
		p.Bool(x)
	case mapi.PtString8:
		x, err := asType[string](v)
		if err != nil {
			return err
		}
		p.String8(x)
	case mapi.PtUnicode:
		x, err := asType[string](v)
		if err != nil {
			return err
		}
		p.Unicode(x)
	case mapi.PtCLSID:
		x, err := asType[mapi.GUID](v)
		if err != nil {
			return err
		}
		p.GUID(x)
	case mapi.PtBinary:
		x, err := asType[[]byte](v)
		if err != nil {
			return err
		}
		return p.Bin(x)
	case mapi.PtObject:
		// PT_OBJECT carries no data in address-book mode; elsewhere it is a
		// binary (e.g. PR_ATTACH_DATA_OBJ during ICS).
		if p.flags&FlagABK != 0 {
			return nil
		}
		x, err := asType[[]byte](v)
		if err != nil {
			return err
		}
		return p.Bin(x)
	case mapi.PtMvShort:
		return pushMV(p, v, func(p *Push, x int16) error { p.Uint16(uint16(x)); return nil })
	case mapi.PtMvLong:
		return pushMV(p, v, func(p *Push, x int32) error { p.Uint32(uint32(x)); return nil })
	case mapi.PtMvCurrency, mapi.PtMvI8:
		return pushMV(p, v, func(p *Push, x int64) error { p.Uint64(uint64(x)); return nil })
	case mapi.PtMvSysTime:
		return pushMV(p, v, func(p *Push, x uint64) error { p.Uint64(x); return nil })
	case mapi.PtMvFloat:
		return pushMV(p, v, func(p *Push, x float32) error { p.Float32(x); return nil })
	case mapi.PtMvDouble, mapi.PtMvAppTime:
		return pushMV(p, v, func(p *Push, x float64) error { p.Float64(x); return nil })
	case mapi.PtMvString8:
		return pushMV(p, v, func(p *Push, x string) error { p.String8(x); return nil })
	case mapi.PtMvUnicode:
		return pushMV(p, v, func(p *Push, x string) error { p.Unicode(x); return nil })
	case mapi.PtMvCLSID:
		return pushMV(p, v, func(p *Push, x mapi.GUID) error { p.GUID(x); return nil })
	case mapi.PtMvBinary:
		return pushMV(p, v, func(p *Push, x []byte) error { return p.Bin(x) })
	default:
		return fmt.Errorf("%w: unsupported property type %s", ErrFormat, typ)
	}
	return nil
}

// PropValue reads a bare property value of the given type.
func (p *Pull) PropValue(typ mapi.PropType) (any, error) {
	if p.flags&FlagABK != 0 && abkGated(typ) {
		valueSet, err := p.Uint8()
		if err != nil {
			return nil, err
		}
		if valueSet == 0 {
			return nil, nil // value absent
		}
		if valueSet != 0xFF {
			return nil, ErrFormat
		}
	} else if typ&mapi.MviFlag == mapi.MviFlag {
		typ &^= mapi.MviFlag
	}
	switch typ {
	case mapi.PtNull:
		return nil, nil // see Push.PropValue: deliberate deviation, no payload
	case mapi.PtUnspecified:
		return p.TypedPropVal()
	case mapi.PtSvrEID:
		return p.SVREID()
	case mapi.PtRestriction:
		return p.Restriction()
	case mapi.PtActions:
		return p.RuleActions()
	case mapi.PtShort:
		v, err := p.Uint16()
		return int16(v), err
	case mapi.PtLong:
		v, err := p.Uint32()
		return int32(v), err
	case mapi.PtError:
		v, err := p.Uint32()
		return v, err
	case mapi.PtFloat:
		return p.Float32()
	case mapi.PtDouble, mapi.PtAppTime:
		return p.Float64()
	case mapi.PtCurrency, mapi.PtI8:
		v, err := p.Uint64()
		return int64(v), err
	case mapi.PtSysTime:
		return p.Uint64()
	case mapi.PtBoolean:
		return p.Bool()
	case mapi.PtString8:
		return p.String8()
	case mapi.PtUnicode:
		return p.Unicode()
	case mapi.PtCLSID:
		return p.GUID()
	case mapi.PtBinary:
		return p.Bin()
	case mapi.PtObject:
		if p.flags&FlagABK != 0 {
			return nil, nil
		}
		return p.Bin()
	case mapi.PtMvShort:
		return pullMV(p, func(p *Pull) (int16, error) { v, err := p.Uint16(); return int16(v), err })
	case mapi.PtMvLong:
		return pullMV(p, func(p *Pull) (int32, error) { v, err := p.Uint32(); return int32(v), err })
	case mapi.PtMvCurrency, mapi.PtMvI8:
		return pullMV(p, func(p *Pull) (int64, error) { v, err := p.Uint64(); return int64(v), err })
	case mapi.PtMvSysTime:
		return pullMV(p, func(p *Pull) (uint64, error) { return p.Uint64() })
	case mapi.PtMvFloat:
		return pullMV(p, func(p *Pull) (float32, error) { return p.Float32() })
	case mapi.PtMvDouble, mapi.PtMvAppTime:
		return pullMV(p, func(p *Pull) (float64, error) { return p.Float64() })
	case mapi.PtMvString8:
		return pullMV(p, func(p *Pull) (string, error) { return p.String8() })
	case mapi.PtMvUnicode:
		return pullMV(p, func(p *Pull) (string, error) { return p.Unicode() })
	case mapi.PtMvCLSID:
		return pullMV(p, func(p *Pull) (mapi.GUID, error) { return p.GUID() })
	case mapi.PtMvBinary:
		return pullMV(p, func(p *Pull) ([]byte, error) { return p.Bin() })
	default:
		return nil, fmt.Errorf("%w: unsupported property type %s", ErrFormat, typ)
	}
}

// TaggedPropVal writes a property tag followed by its value (the value type is
// derived from the tag; it is not self-described on the wire).
func (p *Push) TaggedPropVal(tp mapi.TaggedPropVal) error {
	p.Uint32(uint32(tp.Tag))
	return p.PropValue(tp.Tag.Type(), tp.Value)
}

// TaggedPropVal reads a tagged property value.
func (p *Pull) TaggedPropVal() (mapi.TaggedPropVal, error) {
	tag, err := p.Uint32()
	if err != nil {
		return mapi.TaggedPropVal{}, err
	}
	val, err := p.PropValue(mapi.PropTag(tag).Type())
	return mapi.TaggedPropVal{Tag: mapi.PropTag(tag), Value: val}, err
}

// PropertyValues writes a TPROPVAL_ARRAY: a uint16 count followed by each
// tagged property value.
func (p *Push) PropertyValues(pv mapi.PropertyValues) error {
	if len(pv) > 0xFFFF {
		return ErrFormat
	}
	p.Uint16(uint16(len(pv)))
	for _, tp := range pv {
		if err := p.TaggedPropVal(tp); err != nil {
			return err
		}
	}
	return nil
}

// PropertyValues reads a TPROPVAL_ARRAY.
func (p *Pull) PropertyValues() (mapi.PropertyValues, error) {
	n, err := p.Uint16()
	if err != nil {
		return nil, err
	}
	out := make(mapi.PropertyValues, n)
	for i := range out {
		if out[i], err = p.TaggedPropVal(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PropTags writes a PROPTAG_ARRAY: a uint16 count followed by each 32-bit tag.
func (p *Push) PropTags(tags []mapi.PropTag) error {
	if len(tags) > 0xFFFF {
		return ErrFormat
	}
	p.Uint16(uint16(len(tags)))
	for _, t := range tags {
		p.Uint32(uint32(t))
	}
	return nil
}

// PropTags reads a PROPTAG_ARRAY.
func (p *Pull) PropTags() ([]mapi.PropTag, error) {
	n, err := p.Uint16()
	if err != nil {
		return nil, err
	}
	out := make([]mapi.PropTag, n)
	for i := range out {
		v, err := p.Uint32()
		if err != nil {
			return nil, err
		}
		out[i] = mapi.PropTag(v)
	}
	return out, nil
}
