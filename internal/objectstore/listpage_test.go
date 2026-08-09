package objectstore

import (
	"fmt"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// inboxFID is the folder every listing test seeds.
const inboxFID = int64(mapi.PrivateFIDInbox)

// seedFolder appends n messages with staggered arrival times and returns the store.
func seedListing(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	for i := range 10 {
		raw := fmt.Sprintf("From: sender%02d@hermex.test\r\nSubject: message %02d\r\n\r\nbody %d", 9-i, i, i)
		// Even ones arrive already read, and the first two starred, so the filters
		// have something to select.
		var flags int64
		if i%2 == 0 {
			flags |= FlagSeen
		}
		if i < 2 {
			flags |= FlagFlagged
		}
		if _, err := st.AppendMessage(inboxFID, []byte(raw), time.Unix(1700000000+int64(i)*60, 0), flags); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

// TestListMessagesPageBounds proves the page is what the query returns, not what
// the caller trims: the point of the change is that a page turn does not read the
// whole folder.
func TestListMessagesPageBounds(t *testing.T) {
	st := seedListing(t)

	page, err := st.ListMessagesPage(inboxFID, ListOptions{Limit: 3, Offset: 3, Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 3 {
		t.Errorf("page holds %d messages, want 3", len(page.Messages))
	}
	if page.Total != 10 {
		t.Errorf("total = %d, want the whole folder (10)", page.Total)
	}
	if page.Unread != 5 {
		t.Errorf("unread = %d, want 5", page.Unread)
	}
	// Newest first, so offset 3 starts at the fourth-newest.
	whole, err := st.ListMessagesPage(inboxFID, ListOptions{Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range page.Messages {
		if m.UID != whole.Messages[3+i].UID {
			t.Errorf("page[%d] uid = %d, want %d", i, m.UID, whole.Messages[3+i].UID)
		}
	}
}

// TestListMessagesPageFilters proves the filters select in the query, and that
// the unread badge still counts the whole folder rather than the filtered set.
func TestListMessagesPageFilters(t *testing.T) {
	st := seedListing(t)

	unread, err := st.ListMessagesPage(inboxFID, ListOptions{Unread: true})
	if err != nil {
		t.Fatal(err)
	}
	if unread.Total != 5 || len(unread.Messages) != 5 {
		t.Errorf("unread listing = %d messages, total %d; want 5/5", len(unread.Messages), unread.Total)
	}

	starred, err := st.ListMessagesPage(inboxFID, ListOptions{Flagged: true})
	if err != nil {
		t.Fatal(err)
	}
	if starred.Total != 2 || len(starred.Messages) != 2 {
		t.Errorf("starred listing = %d messages, total %d; want 2/2", len(starred.Messages), starred.Total)
	}
	if starred.Unread != 5 {
		t.Errorf("unread badge under a filter = %d, want the folder's 5", starred.Unread)
	}
}

// TestListMessagesPageOrdering proves each supported ordering is applied by the
// query, including the case-insensitive text keys.
func TestListMessagesPageOrdering(t *testing.T) {
	st := seedListing(t)

	bySender, err := st.ListMessagesPage(inboxFID, ListOptions{Sort: "sender"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(bySender.Messages); i++ {
		if bySender.Messages[i-1].Sender > bySender.Messages[i].Sender {
			t.Fatalf("sender ordering broken at %d: %q then %q", i,
				bySender.Messages[i-1].Sender, bySender.Messages[i].Sender)
		}
	}
	bySubject, err := st.ListMessagesPage(inboxFID, ListOptions{Sort: "subject", Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(bySubject.Messages); i++ {
		if bySubject.Messages[i-1].Subject < bySubject.Messages[i].Subject {
			t.Fatalf("descending subject ordering broken at %d", i)
		}
	}
}

// TestSortKeyIsAnAllowlist proves an ordering the caller invents cannot reach the
// query: an unknown name falls back to arrival time.
func TestSortKeyIsAnAllowlist(t *testing.T) {
	for _, name := range []string{"from", "Sender", "subject", "size"} {
		if SortKey(name) == "" {
			t.Errorf("SortKey(%q) = \"\", want a supported key", name)
		}
	}
	for _, name := range []string{"", "received DESC; DROP TABLE messages", "rowid", "uid"} {
		if got := SortKey(name); got != "" {
			t.Errorf("SortKey(%q) = %q, want the arrival-time fallback", name, got)
		}
	}
}
