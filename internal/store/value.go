package store

import (
	"fmt"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// storeExtFlags freezes the encoding used for complex property values stored as
// blobs. It is a storage-format contract: UTF-8 strings, 16-bit binary counts.
// It must never track a wire-protocol flag change.
const storeExtFlags = ext.Flags(0)

// assertType asserts that v holds a value of type T.
func assertType[T any](v any) (T, error) {
	t, ok := v.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("store: value of type %T is not %T", v, zero)
	}
	return t, nil
}

// encodeStoredValue converts a property value to a SQLite-bindable value:
// scalars become native INTEGER/REAL/TEXT/BLOB so they stay queryable, while
// complex types (multivalue, GUID, server EID, restriction, rule actions) are
// ext-serialized blobs.
func encodeStoredValue(typ mapi.PropType, v any) (any, error) {
	switch typ {
	case mapi.PtBoolean:
		b, err := assertType[bool](v)
		if err != nil {
			return nil, err
		}
		if b {
			return int64(1), nil
		}
		return int64(0), nil
	case mapi.PtShort:
		x, err := assertType[int16](v)
		return int64(x), err
	case mapi.PtLong:
		x, err := assertType[int32](v)
		return int64(x), err
	case mapi.PtError:
		x, err := assertType[uint32](v)
		return int64(x), err
	case mapi.PtI8, mapi.PtCurrency:
		return assertType[int64](v)
	case mapi.PtSysTime:
		x, err := assertType[uint64](v)
		return int64(x), err
	case mapi.PtFloat:
		x, err := assertType[float32](v)
		return float64(x), err
	case mapi.PtDouble, mapi.PtAppTime:
		return assertType[float64](v)
	case mapi.PtString8, mapi.PtUnicode:
		return assertType[string](v)
	case mapi.PtBinary:
		return assertType[[]byte](v)
	default:
		p := ext.NewPush(storeExtFlags)
		if err := p.PropValue(typ, v); err != nil {
			return nil, err
		}
		return p.Bytes(), nil
	}
}

// decodeStoredValue converts a SQLite column value back to the property value
// Go type documented on mapi.TaggedPropVal.
func decodeStoredValue(typ mapi.PropType, col any) (any, error) {
	switch typ {
	case mapi.PtBoolean:
		x, err := assertType[int64](col)
		return x != 0, err
	case mapi.PtShort:
		x, err := assertType[int64](col)
		return int16(x), err
	case mapi.PtLong:
		x, err := assertType[int64](col)
		return int32(x), err
	case mapi.PtError:
		x, err := assertType[int64](col)
		return uint32(x), err
	case mapi.PtI8, mapi.PtCurrency:
		return assertType[int64](col)
	case mapi.PtSysTime:
		x, err := assertType[int64](col)
		return uint64(x), err
	case mapi.PtFloat:
		x, err := assertType[float64](col)
		return float32(x), err
	case mapi.PtDouble, mapi.PtAppTime:
		return assertType[float64](col)
	case mapi.PtString8, mapi.PtUnicode:
		// TEXT columns arrive as string; tolerate []byte defensively.
		switch c := col.(type) {
		case string:
			return c, nil
		case []byte:
			return string(c), nil
		default:
			return nil, fmt.Errorf("store: string property is %T", col)
		}
	case mapi.PtBinary:
		return assertType[[]byte](col)
	default:
		blob, err := assertType[[]byte](col)
		if err != nil {
			return nil, err
		}
		return ext.NewPull(blob, storeExtFlags).PropValue(typ)
	}
}
