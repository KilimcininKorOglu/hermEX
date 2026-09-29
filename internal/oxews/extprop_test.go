package oxews

import (
	"errors"
	"reflect"
	"testing"

	"hermex/internal/mapi"
)

// TestExtendedFieldTarget proves each valid URI spelling resolves to the property
// it names and each invalid combination is refused.
func TestExtendedFieldTarget(t *testing.T) {
	good := []struct {
		uri  ExtendedFieldURI
		want FieldTarget
	}{
		{ExtendedFieldURI{PropertyTag: "0x1000", PropertyType: "String"}, FieldTarget{Type: mapi.PtUnicode, ID: 0x1000}},
		{ExtendedFieldURI{PropertyTag: "4096", PropertyType: "Integer"}, FieldTarget{Type: mapi.PtLong, ID: 0x1000}},
		{ExtendedFieldURI{DistinguishedPropertySetID: "PublicStrings", PropertyName: "x", PropertyType: "String"},
			FieldTarget{Type: mapi.PtUnicode, Named: true, Name: mapi.PropertyName{Kind: mapi.MnidString, GUID: mapi.PsPublicStrings, Name: "x"}}},
		{ExtendedFieldURI{DistinguishedPropertySetID: "Common", PropertyID: "0x8580", PropertyType: "Boolean"},
			FieldTarget{Type: mapi.PtBoolean, Named: true, Name: mapi.PropertyName{Kind: mapi.MnidID, GUID: mapi.PsetidCommon, LID: 0x8580}}},
	}
	for _, c := range good {
		got, err := c.uri.Target()
		if err != nil || got != c.want {
			t.Errorf("Target(%+v) = %+v, %v; want %+v", c.uri, got, err, c.want)
		}
	}
	bad := []ExtendedFieldURI{
		{PropertyTag: "0x1000", PropertyType: "Nope"},
		{PropertyTag: "0x8001", PropertyType: "String"},
		{PropertyTag: "0x1000", DistinguishedPropertySetID: "Common", PropertyType: "String"},
		{DistinguishedPropertySetID: "Common", PropertyType: "String"},
		{DistinguishedPropertySetID: "Common", PropertyName: "a", PropertyID: "1", PropertyType: "String"},
		{DistinguishedPropertySetID: "Common", PropertySetID: "00020329-0000-0000-C000-000000000046", PropertyName: "a", PropertyType: "String"},
	}
	for _, u := range bad {
		if _, err := u.Target(); !errors.Is(err, ErrInvalidExtendedField) {
			t.Errorf("Target(%+v) err = %v, want ErrInvalidExtendedField", u, err)
		}
	}
}

// TestExtendedFieldEcho proves a response never names a distinguished set and a
// set GUID together, and writes a tag in one canonical form.
func TestExtendedFieldEcho(t *testing.T) {
	got := ExtendedFieldURI{DistinguishedPropertySetID: "Common", PropertySetID: "x", PropertyID: "1", PropertyType: "Integer"}.Echo()
	if got.PropertySetID != "" || got.DistinguishedPropertySetID != "Common" {
		t.Errorf("echo = %+v, want the distinguished set alone", got)
	}
	if got := (ExtendedFieldURI{PropertyTag: "4096", PropertyType: "String"}).Echo(); got.PropertyTag != "0x1000" {
		t.Errorf("echoed tag = %q, want 0x1000", got.PropertyTag)
	}
}

// TestExtendedValueRoundTrip proves every supported type survives a render and a
// parse back into the Go type the store keeps.
func TestExtendedValueRoundTrip(t *testing.T) {
	cases := []struct {
		t mapi.PropType
		v any
	}{
		{mapi.PtShort, int16(-3)}, {mapi.PtLong, int32(70000)}, {mapi.PtI8, int64(1) << 40},
		{mapi.PtFloat, float32(1.5)}, {mapi.PtDouble, 2.25}, {mapi.PtBoolean, true},
		{mapi.PtSysTime, mapi.UnixToNTTime(mapi.NTTimeToUnix(133000000000000000))},
		{mapi.PtUnicode, "héllo"}, {mapi.PtBinary, []byte{0, 1, 0xff}},
		{mapi.PtCLSID, mapi.PsetidCommon},
		{mapi.PtMvLong, []int32{1, 2}}, {mapi.PtMvUnicode, []string{"a", "b"}},
	}
	for _, c := range cases {
		val, vals, ok := FormatValue(c.t, c.v)
		if !ok {
			t.Errorf("FormatValue(%v, %v) failed", c.t, c.v)
			continue
		}
		got, err := ParseValue(c.t, ExtendedProperty{Value: val, Values: vals})
		if err != nil || !reflect.DeepEqual(got, c.v) {
			t.Errorf("round trip of %v %v = %v, %v", c.t, c.v, got, err)
		}
	}
	one := "1"
	if _, err := ParseValue(mapi.PtMvLong, ExtendedProperty{Value: &one}); !errors.Is(err, ErrInvalidExtendedField) {
		t.Errorf("an array property with one Value: err = %v", err)
	}
	if _, err := ParseValue(mapi.PtLong, ExtendedProperty{Value: new("x")}); !errors.Is(err, ErrInvalidExtendedField) {
		t.Errorf("a bad integer: err = %v", err)
	}
}
