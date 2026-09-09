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
	// #nosec G115 -- a Go slice length; the buffer it measures is orders of magnitude below the field
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
	if err := p.checkCount(n); err != nil {
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
// the prefix gates strings, binaries, and any multivalue.
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
	write, ok := propWriters[typ]
	if !ok {
		return fmt.Errorf("%w: unsupported property type %s", ErrFormat, typ)
	}
	return write(p, v)
}

// propWriter writes one bare property value. The Go type it expects is the one
// documented on mapi.TaggedPropVal for that property type.
type propWriter func(p *Push, v any) error

// pushTyped adapts a typed writer to an untyped table entry, refusing a value
// that is not the Go type the entry calls for.
func pushTyped[T any](write func(p *Push, x T) error) func(p *Push, v any) error {
	return func(p *Push, v any) error {
		x, err := asType[T](v)
		if err != nil {
			return err
		}
		return write(p, x)
	}
}

// propWriters is the property-type vocabulary the encoder writes. A type absent
// from it is refused rather than guessed at.
var propWriters = map[mapi.PropType]propWriter{
	// Deliberate deviation: the standard property codec has no PT_NULL case and
	// rejects it. PtypNull means "property present, no value", so it is encoded as
	// the empty payload it denotes rather than erroring.
	mapi.PtNull:   func(*Push, any) error { return nil },
	mapi.PtSvrEID: pushTyped((*Push).SVREID),
	mapi.PtShort: pushTyped(func(p *Push, x int16) error {
		p.Uint16(uint16(x)) // #nosec G115 -- the signed and unsigned views of the same 16 bits
		return nil
	}),
	mapi.PtLong: pushTyped(func(p *Push, x int32) error {
		p.Uint32(uint32(x)) // #nosec G115 -- the signed and unsigned views of the same 32 bits
		return nil
	}),
	mapi.PtError:    pushTyped(func(p *Push, x uint32) error { p.Uint32(x); return nil }),
	mapi.PtFloat:    pushTyped(func(p *Push, x float32) error { p.Float32(x); return nil }),
	mapi.PtDouble:   pushDouble,
	mapi.PtAppTime:  pushDouble,
	mapi.PtCurrency: pushInt64,
	mapi.PtI8:       pushInt64,
	mapi.PtSysTime:  pushTyped(func(p *Push, x uint64) error { p.Uint64(x); return nil }),
	mapi.PtBoolean:  pushTyped(func(p *Push, x bool) error { p.Bool(x); return nil }),
	mapi.PtString8:  pushTyped(func(p *Push, x string) error { p.String8(x); return nil }),
	mapi.PtUnicode:  pushTyped(func(p *Push, x string) error { p.Unicode(x); return nil }),
	mapi.PtCLSID:    pushTyped(func(p *Push, x mapi.GUID) error { p.GUID(x); return nil }),
	mapi.PtBinary:   pushTyped((*Push).Bin),
	// PT_OBJECT carries no data in address-book mode; elsewhere it is a binary
	// (e.g. PR_ATTACH_DATA_OBJ during ICS).
	mapi.PtObject: func(p *Push, v any) error {
		if p.flags&FlagABK != 0 {
			return nil
		}
		return pushTyped((*Push).Bin)(p, v)
	},
	mapi.PtMvShort: func(p *Push, v any) error {
		return pushMV(p, v, func(p *Push, x int16) error {
			p.Uint16(uint16(x)) // #nosec G115 -- the signed and unsigned views of the same 16 bits
			return nil
		})
	},
	mapi.PtMvLong: func(p *Push, v any) error {
		return pushMV(p, v, func(p *Push, x int32) error {
			p.Uint32(uint32(x)) // #nosec G115 -- the signed and unsigned views of the same 32 bits
			return nil
		})
	},
	mapi.PtMvCurrency: pushMvInt64,
	mapi.PtMvI8:       pushMvInt64,
	mapi.PtMvDouble:   pushMvDouble,
	mapi.PtMvAppTime:  pushMvDouble,
	mapi.PtMvSysTime: func(p *Push, v any) error {
		return pushMV(p, v, func(p *Push, x uint64) error { p.Uint64(x); return nil })
	},
	mapi.PtMvFloat: func(p *Push, v any) error {
		return pushMV(p, v, func(p *Push, x float32) error { p.Float32(x); return nil })
	},
	mapi.PtMvString8: func(p *Push, v any) error {
		return pushMV(p, v, func(p *Push, x string) error { p.String8(x); return nil })
	},
	mapi.PtMvUnicode: func(p *Push, v any) error {
		return pushMV(p, v, func(p *Push, x string) error { p.Unicode(x); return nil })
	},
	mapi.PtMvCLSID: func(p *Push, v any) error {
		return pushMV(p, v, func(p *Push, x mapi.GUID) error { p.GUID(x); return nil })
	},
	mapi.PtMvBinary: func(p *Push, v any) error {
		return pushMV(p, v, func(p *Push, x []byte) error { return p.Bin(x) })
	},
}

// pushDouble and pushInt64 back the property types that share one encoding:
// PT_APPTIME is a double and PT_I8 is a currency on the wire.
var (
	pushDouble = pushTyped(func(p *Push, x float64) error { p.Float64(x); return nil })
	pushInt64  = pushTyped(func(p *Push, x int64) error {
		p.Uint64(uint64(x)) // #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		return nil
	})
	pushMvDouble propWriter = func(p *Push, v any) error {
		return pushMV(p, v, func(p *Push, x float64) error { p.Float64(x); return nil })
	}
	pushMvInt64 propWriter = func(p *Push, v any) error {
		return pushMV(p, v, func(p *Push, x int64) error {
			p.Uint64(uint64(x)) // #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
			return nil
		})
	}
)

// init registers the writers whose own encoders read propWriters back (a
// restriction and a rule action both carry property values), which a package-level
// table entry cannot express.
func init() {
	propWriters[mapi.PtUnspecified] = pushTyped((*Push).TypedPropVal)
	propWriters[mapi.PtRestriction] = pushTyped((*Push).Restriction)
	propWriters[mapi.PtActions] = pushTyped((*Push).RuleActions)

	propReaders[mapi.PtUnspecified] = pullTyped((*Pull).TypedPropVal)
	propReaders[mapi.PtRestriction] = pullTyped((*Pull).Restriction)
	propReaders[mapi.PtActions] = pullTyped((*Pull).RuleActions)
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
	read, ok := propReaders[typ]
	if !ok {
		return nil, fmt.Errorf("%w: unsupported property type %s", ErrFormat, typ)
	}
	return read(p)
}

// propReader reads one bare property value, returning the Go type documented on
// mapi.TaggedPropVal for that property type.
type propReader func(p *Pull) (any, error)

// pullTyped adapts a typed reader to an untyped table entry.
func pullTyped[T any](read func(p *Pull) (T, error)) func(p *Pull) (any, error) {
	return func(p *Pull) (any, error) { return read(p) }
}

// pullDouble and pullInt64 back the property types that share one encoding:
// PT_APPTIME is a double and PT_I8 is a currency on the wire.
var (
	pullDouble = pullTyped((*Pull).Float64)
	pullInt64  = func(p *Pull) (any, error) {
		v, err := p.Uint64()
		return int64(v), err // #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	}
	pullMvDouble propReader = func(p *Pull) (any, error) {
		return pullMV(p, func(p *Pull) (float64, error) { return p.Float64() })
	}
	pullMvInt64 propReader = func(p *Pull) (any, error) {
		return pullMV(p, func(p *Pull) (int64, error) {
			v, err := p.Uint64()
			return int64(v), err // #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		})
	}
)

// propReaders is the property-type vocabulary the decoder reads. A type absent
// from it is refused rather than guessed at.
var propReaders = map[mapi.PropType]propReader{
	// See Push.PropValue: a deliberate deviation, PT_NULL carries no payload.
	mapi.PtNull:   func(*Pull) (any, error) { return nil, nil },
	mapi.PtSvrEID: pullTyped((*Pull).SVREID),
	mapi.PtShort: func(p *Pull) (any, error) {
		v, err := p.Uint16()
		return int16(v), err // #nosec G115 -- the signed and unsigned views of the same 16 bits
	},
	mapi.PtLong: func(p *Pull) (any, error) {
		v, err := p.Uint32()
		return int32(v), err // #nosec G115 -- the signed and unsigned views of the same 32 bits
	},
	mapi.PtError:    pullTyped((*Pull).Uint32),
	mapi.PtFloat:    pullTyped((*Pull).Float32),
	mapi.PtDouble:   pullDouble,
	mapi.PtAppTime:  pullDouble,
	mapi.PtCurrency: pullInt64,
	mapi.PtI8:       pullInt64,
	mapi.PtSysTime:  pullTyped((*Pull).Uint64),
	mapi.PtBoolean:  pullTyped((*Pull).Bool),
	mapi.PtString8:  pullTyped((*Pull).String8),
	mapi.PtUnicode:  pullTyped((*Pull).Unicode),
	mapi.PtCLSID:    pullTyped((*Pull).GUID),
	mapi.PtBinary:   pullTyped((*Pull).Bin),
	mapi.PtObject: func(p *Pull) (any, error) {
		if p.flags&FlagABK != 0 {
			return nil, nil
		}
		return p.Bin()
	},
	mapi.PtMvShort: func(p *Pull) (any, error) {
		return pullMV(p, func(p *Pull) (int16, error) {
			v, err := p.Uint16()
			return int16(v), err // #nosec G115 -- the signed and unsigned views of the same 16 bits
		})
	},
	mapi.PtMvLong: func(p *Pull) (any, error) {
		return pullMV(p, func(p *Pull) (int32, error) {
			v, err := p.Uint32()
			return int32(v), err // #nosec G115 -- the signed and unsigned views of the same 32 bits
		})
	},
	mapi.PtMvCurrency: pullMvInt64,
	mapi.PtMvI8:       pullMvInt64,
	mapi.PtMvDouble:   pullMvDouble,
	mapi.PtMvAppTime:  pullMvDouble,
	mapi.PtMvSysTime: func(p *Pull) (any, error) {
		return pullMV(p, func(p *Pull) (uint64, error) { return p.Uint64() })
	},
	mapi.PtMvFloat: func(p *Pull) (any, error) {
		return pullMV(p, func(p *Pull) (float32, error) { return p.Float32() })
	},
	mapi.PtMvString8: func(p *Pull) (any, error) {
		return pullMV(p, func(p *Pull) (string, error) { return p.String8() })
	},
	mapi.PtMvUnicode: func(p *Pull) (any, error) {
		return pullMV(p, func(p *Pull) (string, error) { return p.Unicode() })
	},
	mapi.PtMvCLSID: func(p *Pull) (any, error) {
		return pullMV(p, func(p *Pull) (mapi.GUID, error) { return p.GUID() })
	},
	mapi.PtMvBinary: func(p *Pull) (any, error) {
		return pullMV(p, func(p *Pull) ([]byte, error) { return p.Bin() })
	},
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
	// #nosec G115 -- the length is bounded before it reaches the field, by the range check above it or by the 16-bit prefix the bytes were read with
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
	if err := p.checkCount(uint32(n)); err != nil {
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
	// #nosec G115 -- the length is bounded before it reaches the field, by the range check above it or by the 16-bit prefix the bytes were read with
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
	if err := p.checkCount(uint32(n)); err != nil {
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
