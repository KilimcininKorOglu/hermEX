package nspi

import (
	"testing"

	"hermex/internal/mapi"
)

// propEqFilter builds a relational equality restriction on one property tag,
// what a client sends when it filters the address book on an exact value.
func propEqFilter(tag mapi.PropTag, val any) mapi.Restriction {
	return mapi.Restriction{
		Type: mapi.ResProperty,
		Value: mapi.PropertyRestriction{
			Relop:   mapi.RelopEQ,
			PropTag: tag,
			PropVal: mapi.TaggedPropVal{Tag: tag, Value: val},
		},
	}
}

// TestMatchNodeReadsAnsiStringTags pins that an ANSI property tag matches the
// address-book row. A NSPI session is code-page based (CP_WINUNICODE is
// refused), so a client filters with the ANSI tag while the row bag carries the
// unicode one; matching on the exact type alone would never fire.
func TestMatchNodeReadsAnsiStringTags(t *testing.T) {
	u := galUser{mid: midBase, display: "Alice Smith", smtp: "alice@hermex.test"}

	hit := propEqFilter(mapi.PrSmtpAddress.WithType(mapi.PtString8), "alice@hermex.test")
	if !matchNode(u, &hit) {
		t.Error("an ANSI-tagged address filter did not match the entry")
	}

	exists := mapi.Restriction{Type: mapi.ResExist, Value: mapi.ExistRestriction{
		PropTag: mapi.PrDisplayName.WithType(mapi.PtString8),
	}}
	if !matchNode(u, &exists) {
		t.Error("an ANSI-tagged exist filter did not see the display name")
	}

	miss := propEqFilter(mapi.PrSmtpAddress.WithType(mapi.PtString8), "bob@hermex.test")
	if matchNode(u, &miss) {
		t.Error("an ANSI-tagged filter matched a different address")
	}
}

// TestGetMatchesReadsAnsiStringTags is the same guarantee over the wire: the
// request that carries an ANSI tag returns the entry it names.
func TestGetMatchesReadsAnsiStringTags(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test")
	f := propEqFilter(mapi.PrSmtpAddress.WithType(mapi.PtString8), "alice@hermex.test")
	result, mids, _, _ := decodeGetMatches(t, s.GetMatches(buildGetMatches(
		stat{sortType: sortTypeDisplayName, curRec: midBeginningOfTable, codePage: 1252}, &f, 50, nil), testCaller))
	if result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", result)
	}
	if len(mids) != 1 || mids[0] != midBase {
		t.Fatalf("mids = %#x, want [%#x] (alice)", mids, midBase)
	}
}
