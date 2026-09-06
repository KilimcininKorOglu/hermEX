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
	case mapi.PtBoolean, mapi.PtShort, mapi.PtLong, mapi.PtError, mapi.PtI8, mapi.PtCurrency, mapi.PtSysTime:
		return encodeInteger(typ, v)
	case mapi.PtFloat:
		x, err := asType[float32](v)
		return float64(x), err
	case mapi.PtDouble, mapi.PtAppTime:
		return asType[float64](v)
	case mapi.PtString8, mapi.PtUnicode:
		return asType[string](v)
	case mapi.PtBinary:
		return asType[[]byte](v)
	}
	p := ext.NewPush(propExtFlags)
	if err := p.PropValue(typ, v); err != nil {
		return nil, err
	}
	return p.Bytes(), nil
}

// encodeInteger widens the property types SQLite stores in an INTEGER column to
// the int64 the driver binds.
func encodeInteger(typ mapi.PropType, v any) (any, error) {
	switch typ {
	case mapi.PtBoolean:
		b, err := asType[bool](v)
		if err != nil || !b {
			return int64(0), err
		}
		return int64(1), nil
	case mapi.PtShort:
		x, err := asType[int16](v)
		return int64(x), err
	case mapi.PtLong:
		x, err := asType[int32](v)
		return int64(x), err
	case mapi.PtError:
		x, err := asType[uint32](v)
		return int64(x), err
	case mapi.PtSysTime:
		x, err := asType[uint64](v)
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		return int64(x), err
	}
	return asType[int64](v) // PtI8, PtCurrency
}

// decodeValue converts a SQLite column value back to the property value Go type
// documented on mapi.TaggedPropVal.
func decodeValue(typ mapi.PropType, col any) (any, error) {
	switch typ {
	case mapi.PtBoolean, mapi.PtShort, mapi.PtLong, mapi.PtError, mapi.PtI8, mapi.PtCurrency, mapi.PtSysTime:
		return decodeInteger(typ, col)
	case mapi.PtFloat:
		x, err := asType[float64](col)
		return float32(x), err
	case mapi.PtDouble, mapi.PtAppTime:
		return asType[float64](col)
	case mapi.PtString8, mapi.PtUnicode:
		return decodeString(col)
	case mapi.PtBinary:
		return asType[[]byte](col)
	}
	blob, err := asType[[]byte](col)
	if err != nil {
		return nil, err
	}
	return ext.NewPull(blob, propExtFlags).PropValue(typ)
}

// decodeInteger narrows an INTEGER column back to the Go type its property type
// documents. The column is decoded at the type it was encoded with, so it holds
// a value of that width.
func decodeInteger(typ mapi.PropType, col any) (any, error) {
	x, err := asType[int64](col)
	if err != nil {
		return nil, err
	}
	switch typ {
	case mapi.PtBoolean:
		return x != 0, nil
	case mapi.PtShort:
		// #nosec G115 -- the column is decoded at the type it was encoded with, so it holds a value of that width
		return int16(x), nil
	case mapi.PtLong:
		// #nosec G115 -- the column is decoded at the type it was encoded with, so it holds a value of that width
		return int32(x), nil
	case mapi.PtError:
		// #nosec G115 -- the column is decoded at the type it was encoded with, so it holds a value of that width
		return uint32(x), nil
	case mapi.PtSysTime:
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		return uint64(x), nil
	}
	return x, nil // PtI8, PtCurrency
}

// decodeString reads a TEXT column. It arrives as string; []byte is tolerated
// defensively.
func decodeString(col any) (any, error) {
	switch c := col.(type) {
	case string:
		return c, nil
	case []byte:
		return string(c), nil
	}
	return nil, fmt.Errorf("objectstore: string property is %T", col)
}
