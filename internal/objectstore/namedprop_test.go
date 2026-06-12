package objectstore

import (
	"path/filepath"
	"testing"

	"hermex/internal/mapi"
)

var npGUID = mapi.GUID{Data1: 0x11223344, Data2: 0x5566, Data3: 0x7788, Data4: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}}

func TestNamedPropAllocateAndStable(t *testing.T) {
	s := openTestStore(t)

	a := mapi.PropertyName{Kind: mapi.MnidID, GUID: npGUID, LID: 0x8201}
	b := mapi.PropertyName{Kind: mapi.MnidString, GUID: npGUID, Name: "x-custom-header"}

	ids, err := s.GetNamedPropIDs(true, []mapi.PropertyName{a, b})
	if err != nil {
		t.Fatal(err)
	}
	// The first two allocations sit at the base of the named-property range.
	if ids[0] != 0x8000 || ids[1] != 0x8001 {
		t.Fatalf("first allocations = %#x, %#x, want 0x8000, 0x8001", ids[0], ids[1])
	}

	// Re-resolving the same names returns the same ids and allocates nothing new.
	again, err := s.GetNamedPropIDs(true, []mapi.PropertyName{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if again[0] != 0x8001 || again[1] != 0x8000 {
		t.Errorf("re-resolve = %#x, %#x, want 0x8001, 0x8000", again[0], again[1])
	}
	var count int
	if err := s.objdb.QueryRow(`SELECT COUNT(*) FROM named_properties`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("named_properties rows = %d, want 2 (no duplicate allocation)", count)
	}
}

func TestNamedPropCreateFalse(t *testing.T) {
	s := openTestStore(t)
	known := mapi.PropertyName{Kind: mapi.MnidID, GUID: npGUID, LID: 1}
	if _, err := s.GetNamedPropIDs(true, []mapi.PropertyName{known}); err != nil {
		t.Fatal(err)
	}

	unknown := mapi.PropertyName{Kind: mapi.MnidID, GUID: npGUID, LID: 999}
	ids, err := s.GetNamedPropIDs(false, []mapi.PropertyName{known, unknown})
	if err != nil {
		t.Fatal(err)
	}
	if ids[0] != 0x8000 {
		t.Errorf("known name with create=false = %#x, want 0x8000", ids[0])
	}
	if ids[1] != 0 {
		t.Errorf("unknown name with create=false = %#x, want 0 (not allocated)", ids[1])
	}
	var count int
	if err := s.objdb.QueryRow(`SELECT COUNT(*) FROM named_properties`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("named_properties rows = %d, want 1 (create=false allocates nothing)", count)
	}
}

// TestNamedPropDistinctKinds checks that an MNID_ID and an MNID_STRING name in
// the same GUID namespace are distinct, and that an unrepresentable name maps
// to 0.
func TestNamedPropDistinctKinds(t *testing.T) {
	s := openTestStore(t)
	byID := mapi.PropertyName{Kind: mapi.MnidID, GUID: npGUID, LID: 42}
	byName := mapi.PropertyName{Kind: mapi.MnidString, GUID: npGUID, Name: "42"}
	bad := mapi.PropertyName{Kind: mapi.KindNone, GUID: npGUID}

	ids, err := s.GetNamedPropIDs(true, []mapi.PropertyName{byID, byName, bad})
	if err != nil {
		t.Fatal(err)
	}
	if ids[0] == ids[1] {
		t.Errorf("MNID_ID and MNID_STRING collided at %#x", ids[0])
	}
	if ids[0] == 0 || ids[1] == 0 {
		t.Errorf("representable names got 0: %#x, %#x", ids[0], ids[1])
	}
	if ids[2] != 0 {
		t.Errorf("unrepresentable name = %#x, want 0", ids[2])
	}
}

// TestNamedPropStableAcrossReopen checks that allocated ids persist when the
// store is closed and reopened.
func TestNamedPropStableAcrossReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mbox")
	name := mapi.PropertyName{Kind: mapi.MnidString, GUID: npGUID, Name: "persist-me"}

	s1, err := open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := s1.GetNamedPropIDs(true, []mapi.PropertyName{name})
	if err != nil {
		t.Fatal(err)
	}
	first := ids[0]
	s1.Close()

	s2, err := open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	again, err := s2.GetNamedPropIDs(false, []mapi.PropertyName{name})
	if err != nil {
		t.Fatal(err)
	}
	if again[0] != first {
		t.Errorf("id after reopen = %#x, want %#x", again[0], first)
	}
}
