package mapi

import "testing"

// TestRoleMasksMatchSpecHex pins each canonical Outlook permission level to its
// independent [MS-OXCPERM] role-table hex. The roles are symbolic unions of frights
// bits in permission.go; this asserts the union evaluates to the spec value, so a
// dropped or wrong bit in any role definition fails here rather than on the wire.
func TestRoleMasksMatchSpecHex(t *testing.T) {
	cases := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"Reviewer", RightsReviewer, 0x0401},
		{"Contributor", RightsContributor, 0x0402},
		{"NoneditingAuthor", RightsNoneditingAuthor, 0x0413},
		{"Author", RightsAuthor, 0x041B},
		{"PublishingAuthor", RightsPublishingAuthor, 0x049B},
		{"Editor", RightsEditor, 0x047B},
		{"PublishingEditor", RightsPublishingEditor, 0x04FB},
		{"Owner", RightsOwner, 0x05FB},
		{"All", RightsAll, 0x05FB},
		{"MaxROP", RightsMaxROP, 0x1FFB},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = 0x%04X, want 0x%04X", c.name, c.got, c.want)
		}
	}
}

// TestNormalizeRightsImpliedBits checks each implied-rights rule with a vector that
// would fail if that one rule were dropped: a coarse right must imply the narrower
// one it supersedes, and free/busy bits are filled only when the client did not
// claim free/busy control (adjustFreeBusy true).
func TestNormalizeRightsImpliedBits(t *testing.T) {
	cases := []struct {
		name     string
		in       uint32
		adjustFB bool
		want     uint32
	}{
		// ReadAny implies Visible; without adjustFB no free/busy bits appear.
		{"ReadAnyImpliesVisible", FrightsReadAny, false, FrightsReadAny | FrightsVisible},
		// EditAny implies EditOwned.
		{"EditAnyImpliesEditOwned", FrightsEditAny, false, FrightsEditAny | FrightsEditOwned},
		// DeleteAny implies DeleteOwned.
		{"DeleteAnyImpliesDeleteOwned", FrightsDeleteAny, false, FrightsDeleteAny | FrightsDeleteOwned},
		// Owner implies Visible and Contact.
		{"OwnerImpliesVisibleContact", FrightsOwner, false, FrightsOwner | FrightsVisible | FrightsContact},
		// adjustFB with ReadAny grants both free/busy levels (read implies detail).
		{"AdjustFBWithReadAnyGrantsDetailed", FrightsReadAny, true,
			FrightsReadAny | FrightsVisible | FrightsFreeBusySimple | FrightsFreeBusyDetailed},
		// adjustFB without ReadAny grants only the simple free/busy bit.
		{"AdjustFBWithoutReadAnyGrantsSimpleOnly", FrightsCreate, true, FrightsCreate | FrightsFreeBusySimple},
		// INCLUDEFREEBUSY honored (adjustFB false): the server must NOT fill free/busy.
		{"IncludeFreeBusyHonoredNoAutoFill", FrightsCreate, false, FrightsCreate},
	}
	for _, c := range cases {
		if got := NormalizeRights(c.in, c.adjustFB); got != c.want {
			t.Errorf("%s: NormalizeRights(0x%04X, %v) = 0x%04X, want 0x%04X", c.name, c.in, c.adjustFB, got, c.want)
		}
	}
}

// TestRightsMaxROPStripsForbiddenBits proves the ingest mask is security-relevant:
// masking with RightsMaxROP drops bits a client must not set (a reference-private
// 0x2000 store-owner bit and 0x0004 send-as bit) while preserving every legitimate
// right. Without the mask a client could grant itself elevated rights.
func TestRightsMaxROPStripsForbiddenBits(t *testing.T) {
	const forbiddenStoreOwner = 0x2000
	const forbiddenSendAs = 0x0004
	client := RightsOwner | forbiddenStoreOwner | forbiddenSendAs
	if got := client & RightsMaxROP; got != RightsOwner {
		t.Errorf("masked client rights = 0x%04X, want 0x%04X (forbidden bits not stripped)", got, RightsOwner)
	}
	// Every legitimate bit survives the mask.
	if RightsMaxROP&RightsAll != RightsAll {
		t.Error("RightsMaxROP drops a standard right it should keep")
	}
}

// TestSpecialMemberIDWireEncoding pins the special PR_MEMBER_ID values: default is 0
// and anonymous is -1, which as the PtI8 (signed 64-bit) wire value is the all-ones
// pattern. A client always sends these for the default/anonymous members.
func TestSpecialMemberIDWireEncoding(t *testing.T) {
	if MemberIDDefault != 0 {
		t.Errorf("MemberIDDefault = %d, want 0", MemberIDDefault)
	}
	anon := MemberIDAnonymous // runtime int64(-1)→uint64 is the two's-complement wire pattern
	if got := uint64(anon); got != 0xFFFFFFFFFFFFFFFF {
		t.Errorf("anonymous wire value = 0x%016X, want 0xFFFFFFFFFFFFFFFF", got)
	}
}

// TestMemberProptags pins the permission-row proptag values (propid<<16 | proptype).
func TestMemberProptags(t *testing.T) {
	cases := []struct {
		name string
		got  PropTag
		want PropTag
	}{
		{"PrMemberID", PrMemberID, 0x66710014},
		{"PrMemberName", PrMemberName, 0x6672001F},
		{"PrMemberNameA", PrMemberNameA, 0x6672001E},
		{"PrMemberRights", PrMemberRights, 0x66730003},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = 0x%08X, want 0x%08X", c.name, uint32(c.got), uint32(c.want))
		}
	}
}
