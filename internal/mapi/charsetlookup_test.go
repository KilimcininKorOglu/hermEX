package mapi

import "testing"

// TestGetAnyCharsetReadsTheSiblingStringType pins the fallback in both
// directions: the same property stored under one string type must be readable
// through the other, because a client names the string tag it prefers while the
// stored bag carries the one its writer used.
func TestGetAnyCharsetReadsTheSiblingStringType(t *testing.T) {
	string8 := PropertyValues{{Tag: PrSubject.WithType(PtString8), Value: "quarterly"}}
	if v, ok := string8.GetAnyCharset(PrSubject); !ok || v != "quarterly" {
		t.Errorf("unicode tag against a string8 value = (%v, %v), want (quarterly, true)", v, ok)
	}

	unicode := PropertyValues{{Tag: PrSubject, Value: "quarterly"}}
	if v, ok := unicode.GetAnyCharset(PrSubject.WithType(PtString8)); !ok || v != "quarterly" {
		t.Errorf("string8 tag against a unicode value = (%v, %v), want (quarterly, true)", v, ok)
	}
}

// TestGetAnyCharsetPrefersTheExactTag confirms the fallback is a fallback: a bag
// holding both representations answers with the one the caller asked for.
func TestGetAnyCharsetPrefersTheExactTag(t *testing.T) {
	pv := PropertyValues{
		{Tag: PrSubject.WithType(PtString8), Value: "ansi"},
		{Tag: PrSubject, Value: "unicode"},
	}
	if v, _ := pv.GetAnyCharset(PrSubject); v != "unicode" {
		t.Errorf("unicode lookup = %v, want unicode", v)
	}
	if v, _ := pv.GetAnyCharset(PrSubject.WithType(PtString8)); v != "ansi" {
		t.Errorf("string8 lookup = %v, want ansi", v)
	}
}

// TestGetAnyCharsetDoesNotCrossNonStringTypes keeps the fallback to the two
// string types. A tag of any other type must miss rather than read a same-id
// property of a type it cannot interpret.
func TestGetAnyCharsetDoesNotCrossNonStringTypes(t *testing.T) {
	pv := PropertyValues{{Tag: PrSubject, Value: "quarterly"}}
	if v, ok := pv.GetAnyCharset(PrSubject.WithType(PtLong)); ok {
		t.Errorf("long-typed lookup = (%v, true), want a miss", v)
	}
	if _, isString := CharsetSiblingTag(PrSubject.WithType(PtLong)); isString {
		t.Error("a PtLong tag reported a charset sibling")
	}
}
