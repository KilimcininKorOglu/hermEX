package objectstore

import (
	"testing"

	"hermex/internal/mapi"
)

// TestForeignNamesStopAtTheQuota proves a store stops allocating ids for
// sender-chosen names at its quota, while a name it already holds keeps resolving
// and the store's own allocations are unaffected.
func TestForeignNamesStopAtTheQuota(t *testing.T) {
	s := openTestStore(t)
	known := mapi.PropertyName{Kind: mapi.MnidID, GUID: npGUID, LID: 1}
	ids, err := s.GetNamedPropIDs(true, []mapi.PropertyName{known})
	if err != nil {
		t.Fatal(err)
	}
	fill := make([]mapi.PropertyName, foreignNamedPropQuota)
	for i := range fill {
		fill[i] = mapi.PropertyName{Kind: mapi.MnidID, GUID: npGUID, LID: uint32(1000 + i)}
	}
	if _, err := s.GetForeignNamedPropIDs(fill); err != nil {
		t.Fatal(err)
	}

	over := mapi.PropertyName{Kind: mapi.MnidID, GUID: npGUID, LID: 99999}
	got, err := s.GetForeignNamedPropIDs([]mapi.PropertyName{over, known})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 0 {
		t.Errorf("a sender-chosen name past the quota was allocated %#x", got[0])
	}
	if got[1] != ids[0] {
		t.Errorf("a known name resolved to %#x, want %#x", got[1], ids[0])
	}
	own, err := s.GetNamedPropIDs(true, []mapi.PropertyName{over})
	if err != nil || own[0] == 0 {
		t.Errorf("the store's own allocation failed at the foreign quota: %v", err)
	}
}
