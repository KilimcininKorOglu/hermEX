package objectstore

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// storeFlagged creates one calendar object carrying the boolean tag at the given
// value, and returns its id.
func storeFlagged(t *testing.T, s *Store, tag mapi.PropTag, on bool) int64 {
	t.Helper()
	id, err := s.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"},
		{Tag: tag, Value: on},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestListFolderObjectsWithFlag proves the store answers "which objects carry this
// flag" itself: a recurring appointment cannot be found by a time window, because
// the master carries only its first instance's time.
func TestListFolderObjectsWithFlag(t *testing.T) {
	s := openSeededStore(t)
	ids, err := s.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameRecurring})
	if err != nil {
		t.Fatal(err)
	}
	recur := mapi.MakeTag(ids[0], mapi.PtBoolean)

	want := storeFlagged(t, s, recur, true)
	storeFlagged(t, s, recur, false)
	// An object with no such property at all must not be reported either.
	if _, err := s.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"},
	}}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListFolderObjectsWithFlag(int64(mapi.PrivateFIDCalendar), recur)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != want {
		t.Errorf("flagged objects = %+v, want only %d", got, want)
	}
}

// TestListFolderObjectsWithFlagEmpty proves a folder with nothing flagged answers
// nothing rather than everything.
func TestListFolderObjectsWithFlagEmpty(t *testing.T) {
	s := openSeededStore(t)
	ids, _ := s.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameRecurring})
	recur := mapi.MakeTag(ids[0], mapi.PtBoolean)
	storeFlagged(t, s, recur, false)

	got, err := s.ListFolderObjectsWithFlag(int64(mapi.PrivateFIDCalendar), recur)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("flagged objects = %+v, want none", got)
	}
}
