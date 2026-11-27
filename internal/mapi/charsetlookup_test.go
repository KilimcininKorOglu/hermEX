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
	if v, ok := pv.GetAnyCharset(PrSubject); !ok || v != "unicode" {
		t.Errorf("unicode lookup = (%v, %v), want (unicode, true)", v, ok)
	}
	if v, ok := pv.GetAnyCharset(PrSubject.WithType(PtString8)); !ok || v != "ansi" {
		t.Errorf("string8 lookup = (%v, %v), want (ansi, true)", v, ok)
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
	long := PrSubject.WithType(PtLong)
	if sibling, isString := CharsetSiblingTag(long); isString || sibling != long {
		t.Errorf("CharsetSiblingTag(PtLong) = (%#x, %v), want (%#x, false)", uint32(sibling), isString, uint32(long))
	}
}

// TestHasMatchesTheExactTagOnly pins Has to the exact tag (MS-OXCDATA keys a
// property by its full tag): a stored value is found, an absent tag and the
// sibling string type of a stored one are not, and a stored nil value still
// counts as present.
func TestHasMatchesTheExactTagOnly(t *testing.T) {
	pv := PropertyValues{{Tag: PrSubject, Value: "quarterly"}, {Tag: PrBody, Value: nil}}
	if !pv.Has(PrSubject) {
		t.Error("Has(PrSubject) = false for a stored subject")
	}
	if !pv.Has(PrBody) {
		t.Error("Has(PrBody) = false for a property stored with a nil value")
	}
	if pv.Has(PrSubject.WithType(PtString8)) {
		t.Error("Has matched the string8 sibling of a unicode subject")
	}
	if pv.Has(PrDisplayName) {
		t.Error("Has(PrDisplayName) = true for a tag that is not stored")
	}
}
