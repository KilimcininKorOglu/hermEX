package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestCountMessages checks the folder counters agree with ListMessages: total
// equals the listed row count and unread counts exactly the messages whose
// \Seen flag is clear. The invariant matters because the sidebar badge
// (CountMessages) and the rendered list (ListMessages) must never disagree.
func TestCountMessages(t *testing.T) {
	s := openSeededStore(t)

	raw := func(subject string) []byte {
		return []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: " + subject +
			"\r\nDate: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nbody.\r\n")
	}

	// Empty folder: zero of each.
	total, unread := mustCountMessages(t, s, mapi.PrivateFIDInbox)
	wantEq(t, "empty folder total", total, 0)
	wantEq(t, "empty folder unread", unread, 0)

	// Three messages: two unread, one already seen.
	d := time.Unix(1700000000, 0)
	for i, flags := range []int64{0, 0, FlagSeen} {
		mustAppendMessage(t, s, mapi.PrivateFIDInbox, raw(string(rune('a'+i))), d, flags)
	}

	total, unread = mustCountMessages(t, s, mapi.PrivateFIDInbox)
	wantEq(t, "total", total, 3)
	wantEq(t, "unread", unread, 2)

	// Invariant: counts must match what ListMessages enumerates.
	list := mustListMessages(t, s, mapi.PrivateFIDInbox)
	listedUnread := 0
	for _, m := range list {
		if m.Flags&FlagSeen == 0 {
			listedUnread++
		}
	}
	wantEq(t, "total against len(ListMessages)", total, len(list))
	wantEq(t, "unread against the ListMessages tally", unread, listedUnread)
}

// mustCountMessages returns a folder's total and unread counters.
func mustCountMessages(t *testing.T, s *Store, folderID int64) (total, unread int) {
	t.Helper()
	total, unread, err := s.CountMessages(folderID)
	mustNoErr(t, "count messages", err)
	return total, unread
}
