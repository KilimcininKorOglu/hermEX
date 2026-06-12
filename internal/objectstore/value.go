package objectstore

import (
	"fmt"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// propExtFlags freezes the encoding used for complex property values stored as
// blobs in the property tables. It is a storage-format contract (UTF-8 strings,
// 16-bit binary counts) and must never track a wire-protocol flag change.
const propExtFlags = ext.Flags(0)

// asType asserts that v holds a value of type T.
func asType[T any](v any) (T, error) {
	t, ok := v.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("objectstore: value of type %T is not %T", v, zero)
	}
	return t, nil
}

// encodeValue converts a property value to a SQLite-bindable value: scalars
// become native INTEGER/REAL/TEXT/BLOB so they stay queryable, while complex
// types (multivalue, GUID, server EID, restriction, rule actions) are
// length-prefixed blobs.
func encodeValue(typ mapi.PropType, v any) (any, error) {
	switch typ {
	case mapi.PtBoolean:
		b, err := asType[bool](v)
		if err != nil {
			return nil, err
		}
		if b {
			return int64(1), nil
		}
		return int64(0), nil
	case mapi.PtShort:
		x, err := asType[int16](v)
		return int64(x), err
	case mapi.PtLong:
		x, err := asType[int32](v)
		return int64(x), err
	case mapi.PtError:
		x, err := asType[uint32](v)
		return int64(x), err
	case mapi.PtI8, mapi.PtCurrency:
		return asType[int64](v)
	case mapi.PtSysTime:
		x, err := asType[uint64](v)
		return int64(x), err
	case mapi.PtFloat:
		x, err := asType[float32](v)
		return float64(x), err
	case mapi.PtDouble, mapi.PtAppTime:
		return asType[float64](v)
	case mapi.PtString8, mapi.PtUnicode:
		return asType[string](v)
	case mapi.PtBinary:
		return asType[[]byte](v)
	default:
		p := ext.NewPush(propExtFlags)
		if err := p.PropValue(typ, v); err != nil {
			return nil, err
		}
		return p.Bytes(), nil
	}
}

// decodeValue converts a SQLite column value back to the property value Go type
// documented on mapi.TaggedPropVal.
func decodeValue(typ mapi.PropType, col any) (any, error) {
	switch typ {
	case mapi.PtBoolean:
		x, err := asType[int64](col)
		return x != 0, err
	case mapi.PtShort:
		x, err := asType[int64](col)
		return int16(x), err
	case mapi.PtLong:
		x, err := asType[int64](col)
		return int32(x), err
	case mapi.PtError:
		x, err := asType[int64](col)
		return uint32(x), err
	case mapi.PtI8, mapi.PtCurrency:
		return asType[int64](col)
	case mapi.PtSysTime:
		x, err := asType[int64](col)
		return uint64(x), err
	case mapi.PtFloat:
		x, err := asType[float64](col)
		return float32(x), err
	case mapi.PtDouble, mapi.PtAppTime:
		return asType[float64](col)
	case mapi.PtString8, mapi.PtUnicode:
		// TEXT columns arrive as string; tolerate []byte defensively.
		switch c := col.(type) {
		case string:
			return c, nil
		case []byte:
			return string(c), nil
		default:
			return nil, fmt.Errorf("objectstore: string property is %T", col)
		}
	case mapi.PtBinary:
		return asType[[]byte](col)
	default:
		blob, err := asType[[]byte](col)
		if err != nil {
			return nil, err
		}
		return ext.NewPull(blob, propExtFlags).PropValue(typ)
	}
}
