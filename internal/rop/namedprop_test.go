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

	getIDs := func(flags uint8, names []mapi.PropertyName) ([]uint16, uint32) {
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

	// Create an id for the name.
	ids, ec := getIDs(mapiCreate, []mapi.PropertyName{name})
	if ec != ecSuccess || len(ids) != 1 {
		t.Fatalf("GetPropertyIdsFromNames(create) = ids %v (ec %#x)", ids, ec)
	}
	id := ids[0]
	if id < 0x8000 {
		t.Fatalf("allocated named prop id = %#x, want >= 0x8000", id)
	}

	// Resolving the same name without create yields the same id.
	if got, _ := getIDs(0x00, []mapi.PropertyName{name}); len(got) != 1 || got[0] != id {
		t.Errorf("resolve-only of a known name = %v, want [%#x]", got, id)
	}
	// An unknown name without create resolves to 0.
	if got, _ := getIDs(0x00, []mapi.PropertyName{unknown}); len(got) != 1 || got[0] != 0 {
		t.Errorf("resolve-only of an unknown name = %v, want [0]", got)
	}
	// An invalid flag is rejected.
	if _, ec := getIDs(0x01, []mapi.PropertyName{name}); ec != ecInvalidParam {
		t.Errorf("GetPropertyIdsFromNames(flag 0x01) ec = %#x, want ecInvalidParam", ec)
	}

	// Recover the name from the id.
	resp, _ := sess.Dispatch(buildGetNamesFromPropertyIds(0, []uint16{id, 0x0037}), []uint32{logonH})
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
	if len(names) != 2 {
		t.Fatalf("recovered %d names, want 2", len(names))
	}
	if names[0].Kind != mapi.MnidString || names[0].Name != "hermexTestKeyword" || names[0].GUID != mapi.PsPublicStrings {
		t.Errorf("recovered name = %+v, want the created MnidString name", names[0])
	}
	// A static property id (PR_SUBJECT's id 0x0037) has no named mapping.
	if names[1].Kind != mapi.KindNone {
		t.Errorf("unmapped id recovered as kind %d, want KindNone (%d)", names[1].Kind, mapi.KindNone)
	}
}
