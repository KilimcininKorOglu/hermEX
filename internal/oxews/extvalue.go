package oxews

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"hermex/internal/mapi"
)

// scalarCodec renders one value of a scalar MAPI type as the text of a <t:Value>
// and reads it back into the Go type the store keeps for that type.
type scalarCodec struct {
	format func(v any) (string, bool)
	parse  func(s string) (any, error)
	// slice gathers parsed values into the store's array type for the array form.
	slice func(vs []any) any
}

// scalarCodecs are the codecs by scalar type. PtAppTime and PtCurrency share the
// encodings of PtDouble and PtI8, as they do on the MAPI wire.
var scalarCodecs = map[mapi.PropType]scalarCodec{
	mapi.PtShort:    {formatInt, parseIntBits(16, func(n int64) any { return int16(n) }), sliceOf[int16]},
	mapi.PtLong:     {formatInt, parseIntBits(32, func(n int64) any { return int32(n) }), sliceOf[int32]},
	mapi.PtI8:       {formatInt, parseIntBits(64, func(n int64) any { return n }), sliceOf[int64]},
	mapi.PtCurrency: {formatInt, parseIntBits(64, func(n int64) any { return n }), sliceOf[int64]},
	mapi.PtError:    {formatInt, parseError, nil},
	mapi.PtFloat:    {formatFloat, parseFloat32, sliceOf[float32]},
	mapi.PtDouble:   {formatFloat, parseFloat64, sliceOf[float64]},
	mapi.PtAppTime:  {formatFloat, parseFloat64, sliceOf[float64]},
	mapi.PtBoolean:  {formatBool, parseBool, nil},
	mapi.PtSysTime:  {formatTime, parseTime, sliceOf[uint64]},
	mapi.PtUnicode:  {formatString, parseString, sliceOf[string]},
	mapi.PtBinary:   {formatBinary, parseBinary, sliceOf[[]byte]},
	mapi.PtCLSID:    {formatGUID, parseGUID, sliceOf[mapi.GUID]},
}

// FormatValue renders a stored value of type t as the value or values of an
// <t:ExtendedProperty>. ok is false for a value this server cannot render.
func FormatValue(t mapi.PropType, v any) (value *string, values *ExtendedValues, ok bool) {
	c, known := scalarCodecs[t.Base()]
	if !known {
		return nil, nil, false
	}
	if !t.IsMultivalue() {
		s, ok := c.format(v)
		return &s, nil, ok
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		return nil, nil, false
	}
	out := &ExtendedValues{Value: make([]string, 0, rv.Len())}
	for i := range rv.Len() {
		s, ok := c.format(rv.Index(i).Interface())
		if !ok {
			return nil, nil, false
		}
		out.Value = append(out.Value, s)
	}
	return nil, out, true
}

// ParseValue reads the value of an <t:ExtendedProperty> into the Go value the
// store keeps for type t. A scalar type takes <t:Value> and an array type takes
// <t:Values>; the other form is refused.
func ParseValue(t mapi.PropType, p ExtendedProperty) (any, error) {
	c, known := scalarCodecs[t.Base()]
	if !known || (t.IsMultivalue() && c.slice == nil) {
		return nil, fmt.Errorf("%w: property type %s", ErrInvalidExtendedField, t)
	}
	if !t.IsMultivalue() {
		if p.Value == nil || p.Values != nil {
			return nil, fmt.Errorf("%w: a single-valued property needs one Value", ErrInvalidExtendedField)
		}
		return c.parse(*p.Value)
	}
	if p.Values == nil || p.Value != nil {
		return nil, fmt.Errorf("%w: an array property needs Values", ErrInvalidExtendedField)
	}
	return parseArray(c, p.Values.Value)
}

// parseArray reads the <t:Values> of an array property into the store's slice type.
func parseArray(c scalarCodec, values []string) (any, error) {
	vs := make([]any, 0, len(values))
	for _, s := range values {
		v, err := c.parse(s)
		if err != nil {
			return nil, err
		}
		vs = append(vs, v)
	}
	return c.slice(vs), nil
}

// sliceOf gathers parsed values of one Go type into a slice of that type.
func sliceOf[T any](vs []any) any {
	out := make([]T, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.(T))
	}
	return out
}

// formatInt renders any integer the store may hand back.
func formatInt(v any) (string, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(rv.Uint(), 10), true
	}
	return "", false
}

// parseIntBits reads a decimal integer that fits in bits and converts it to the
// store's Go type.
func parseIntBits(bits int, conv func(int64) any) func(string) (any, error) {
	return func(s string) (any, error) {
		n, err := strconv.ParseInt(s, 10, bits)
		if err != nil {
			return nil, fmt.Errorf("%w: integer %q", ErrInvalidExtendedField, s)
		}
		return conv(n), nil
	}
}

// parseError reads an error code, a 32-bit unsigned integer.
func parseError(s string) (any, error) {
	n, err := strconv.ParseUint(s, 0, 32)
	if err != nil {
		return nil, fmt.Errorf("%w: error code %q", ErrInvalidExtendedField, s)
	}
	return uint32(n), nil
}

// formatFloat renders a float32 or float64.
func formatFloat(v any) (string, bool) {
	switch f := v.(type) {
	case float32:
		return strconv.FormatFloat(float64(f), 'g', -1, 32), true
	case float64:
		return strconv.FormatFloat(f, 'g', -1, 64), true
	}
	return "", false
}

func parseFloat32(s string) (any, error) {
	f, err := strconv.ParseFloat(s, 32)
	if err != nil {
		return nil, fmt.Errorf("%w: number %q", ErrInvalidExtendedField, s)
	}
	return float32(f), nil
}

func parseFloat64(s string) (any, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: number %q", ErrInvalidExtendedField, s)
	}
	return f, nil
}

func formatBool(v any) (string, bool) {
	b, ok := v.(bool)
	return strconv.FormatBool(b), ok
}

func parseBool(s string) (any, error) {
	b, err := strconv.ParseBool(s)
	if err != nil {
		return nil, fmt.Errorf("%w: boolean %q", ErrInvalidExtendedField, s)
	}
	return b, nil
}

// formatTime renders a FILETIME, or a time the store already converted, as an
// xs:dateTime in UTC.
func formatTime(v any) (string, bool) {
	switch t := v.(type) {
	case uint64:
		return mapi.NTTimeToUnix(t).Format(ewsTime), true
	case time.Time:
		return t.UTC().Format(ewsTime), true
	}
	return "", false
}

// parseTime reads an xs:dateTime into a FILETIME. A value with no zone is UTC.
func parseTime(s string) (any, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return mapi.UnixToNTTime(t), nil
		}
	}
	return nil, fmt.Errorf("%w: date and time %q", ErrInvalidExtendedField, s)
}

func formatString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func parseString(s string) (any, error) { return s, nil }

func formatBinary(v any) (string, bool) {
	b, ok := v.([]byte)
	return base64.StdEncoding.EncodeToString(b), ok
}

func parseBinary(s string) (any, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: base64 value", ErrInvalidExtendedField)
	}
	return b, nil
}

func formatGUID(v any) (string, bool) {
	g, ok := v.(mapi.GUID)
	return g.String(), ok
}

func parseGUID(s string) (any, error) {
	g, err := mapi.ParseGUID(s)
	if err != nil {
		return nil, fmt.Errorf("%w: GUID %q", ErrInvalidExtendedField, s)
	}
	return g, nil
}
