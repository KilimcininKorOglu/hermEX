package objectstore

import (
	"fmt"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// seedListIDs appends n messages to the Inbox and returns the open store.
func seedListIDs(t *testing.T, n int) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for i := range n {
		raw := fmt.Sprintf("From: s%d@x.test\r\nTo: a@x.test\r\nSubject: Row %d\r\n\r\nbody\r\n", i, i)
		if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(1718200000+int64(i), 0), 0); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	return st
}

// wantSameIDs fails the test unless got holds exactly the ids of want, in order.
func wantSameIDs(t *testing.T, got []int64, want []MessageInfo, what string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d ids, want %d", what, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i].ID {
			t.Errorf("%s: id %d = %d, want %d", what, i, got[i], want[i].ID)
		}
	}
}

// TestListMessageIDsMatchesListMessages proves the id reader returns the same
// rows in the same order as the full reader, which is what lets a caller that only
// addresses rows use the cheaper one.
func TestListMessageIDsMatchesListMessages(t *testing.T) {
	st := seedListIDs(t, 5)

	full, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	ids, err := st.ListMessageIDs(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	wantSameIDs(t, ids, full, "ListMessageIDs")
}

// TestListMessageIDsEmptyFolder proves an empty folder yields no ids and no error,
// because a caller sizes its table from the result.
func TestListMessageIDsEmptyFolder(t *testing.T) {
	st := seedListIDs(t, 0)

	ids, err := st.ListMessageIDs(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("got %d ids for an empty folder, want 0", len(ids))
	}
}

// TestListSoftDeletedIDsMatchesListSoftDeletedInfo proves the dumpster id reader
// agrees with the full one, and that a live message is not among the ids.
func TestListSoftDeletedIDsMatchesListSoftDeletedInfo(t *testing.T) {
	st := seedListIDs(t, 3)

	live, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 3 {
		t.Fatalf("seeded %d messages, want 3", len(live))
	}
	// Soft-delete the first two; the third stays live.
	for _, m := range live[:2] {
		if err := st.SoftDeleteMessage(int64(mapi.PrivateFIDInbox), m.UID); err != nil {
			t.Fatal(err)
		}
	}

	full, err := st.ListSoftDeletedInfo(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	ids, err := st.ListSoftDeletedIDs(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	wantSameIDs(t, ids, full, "ListSoftDeletedIDs")
	if len(ids) != 2 {
		t.Fatalf("got %d dumpster ids, want 2", len(ids))
	}
	for _, id := range ids {
		if id == live[2].ID {
			t.Errorf("the live message %d is among the dumpster ids", id)
		}
	}
}
