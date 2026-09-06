package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// buildGetPropertyIdsFromNames builds a RopGetPropertyIdsFromNames request (Flags
// u8, then a PROPNAME_ARRAY).
func buildGetPropertyIdsFromNames(inIdx, flags uint8, names []mapi.PropertyName) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropGetPropertyIdsFromNames)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(flags)
	_ = b.PropertyNames(names)
	return b.Bytes()
}

// buildGetNamesFromPropertyIds builds a RopGetNamesFromPropertyIds request (a
// PROPID_ARRAY).
func buildGetNamesFromPropertyIds(inIdx uint8, ids []uint16) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropGetNamesFromPropertyIds)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	_ = b.PropIDs(ids)
	return b.Bytes()
}

// TestNamedPropertyRoundTrip drives RopGetPropertyIdsFromNames and
// RopGetNamesFromPropertyIds against the store's named-property map: creating an id
// for a name, resolving the same name without create to the same id, resolving an
// unknown name to 0, recovering the name from the id, getting the "none" kind for an
// unmapped (static) id, and rejecting an invalid flag.
func TestNamedPropertyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	name := mapi.PropertyName{Kind: mapi.MnidString, GUID: mapi.PsPublicStrings, Name: "hermexTestKeyword"}
	unknown := mapi.PropertyName{Kind: mapi.MnidString, GUID: mapi.PsPublicStrings, Name: "neverSeenName"}

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	// Create an id for the name.
	ids, ec := idsFromNames(t, sess, logonH, mapiCreate, name)
	if ec != ecSuccess || len(ids) != 1 {
		t.Fatalf("GetPropertyIdsFromNames(create) = ids %v (ec %#x)", ids, ec)
	}
	id := ids[0]
	if id < 0x8000 {
		t.Fatalf("allocated named prop id = %#x, want >= 0x8000", id)
	}

	// Resolving the same name without create yields the same id.
	wantIDs(t, sess, logonH, "a known name", 0x00, name, id)
	// An unknown name without create resolves to 0.
	wantIDs(t, sess, logonH, "an unknown name", 0x00, unknown, 0)
	// An invalid flag is rejected.
	if _, ec := idsFromNames(t, sess, logonH, 0x01, name); ec != ecInvalidParam {
		t.Errorf("GetPropertyIdsFromNames(flag 0x01) ec = %#x, want ecInvalidParam", ec)
	}

	// Recover the name from the id, alongside a static property id (PR_SUBJECT's
	// 0x0037) that has no named mapping.
	assertRecoveredNames(t, namesFromIDs(t, sess, logonH, []uint16{id, 0x0037}))
}

// wantIDs asserts a resolve-only lookup of one name yields exactly one id.
func wantIDs(t *testing.T, sess *Session, logonH uint32, what string, flags uint8, name mapi.PropertyName, want uint16) {
	t.Helper()
	if got, _ := idsFromNames(t, sess, logonH, flags, name); len(got) != 1 || got[0] != want {
		t.Errorf("resolve-only of %s = %v, want [%#x]", what, got, want)
	}
}

// assertRecoveredNames checks the pair RopGetNamesFromPropertyIds returned: the
// created MnidString name, then the "none" kind a static id maps to.
func assertRecoveredNames(t *testing.T, names []mapi.PropertyName) {
	t.Helper()
	if len(names) != 2 {
		t.Fatalf("recovered %d names, want 2", len(names))
	}
	if names[0].Kind != mapi.MnidString || names[0].Name != "hermexTestKeyword" || names[0].GUID != mapi.PsPublicStrings {
		t.Errorf("recovered name = %+v, want the created MnidString name", names[0])
	}
	if names[1].Kind != mapi.KindNone {
		t.Errorf("unmapped id recovered as kind %d, want KindNone (%d)", names[1].Kind, mapi.KindNone)
	}
}

// idsFromNames runs RopGetPropertyIdsFromNames and returns the ids it resolved
// along with the ROP's return code. A non-success return carries no id array.
func idsFromNames(t *testing.T, sess *Session, logonH uint32, flags uint8, names ...mapi.PropertyName) ([]uint16, uint32) {
	t.Helper()
	resp, _ := sess.Dispatch(buildGetPropertyIdsFromNames(0, flags, names), []uint32{logonH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	ec := mustU32(t, p, "ec")
	if ec != ecSuccess {
		return nil, ec
	}
	ids, err := p.PropIDs()
	if err != nil {
		t.Fatalf("PROPID_ARRAY: %v", err)
	}
	return ids, ec
}

// namesFromIDs runs RopGetNamesFromPropertyIds and returns the recovered names.
func namesFromIDs(t *testing.T, sess *Session, logonH uint32, ids []uint16) []mapi.PropertyName {
	t.Helper()
	resp, _ := sess.Dispatch(buildGetNamesFromPropertyIds(0, ids), []uint32{logonH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("GetNamesFromPropertyIds ReturnValue = %#x", ec)
	}
	names, err := p.PropertyNames()
	if err != nil {
		t.Fatalf("PROPNAME_ARRAY: %v", err)
	}
	return names
}
