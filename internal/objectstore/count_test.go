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
	if total, unread, err := s.CountMessages(mapi.PrivateFIDInbox); err != nil {
		t.Fatal(err)
	} else if total != 0 || unread != 0 {
		t.Fatalf("empty folder counts = (%d,%d), want (0,0)", total, unread)
	}

	// Three messages: two unread, one already seen.
	d := time.Unix(1700000000, 0)
	for i, flags := range []int64{0, 0, FlagSeen} {
		if _, err := s.AppendMessage(mapi.PrivateFIDInbox, raw(string(rune('a'+i))), d, flags); err != nil {
			t.Fatal(err)
		}
	}

	total, unread, err := s.CountMessages(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || unread != 2 {
		t.Errorf("counts = (%d,%d), want (3,2)", total, unread)
	}

	// Invariant: counts must match what ListMessages enumerates.
	list, err := s.ListMessages(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	wantUnread := 0
	for _, m := range list {
		if m.Flags&FlagSeen == 0 {
			wantUnread++
		}
	}
	if total != len(list) {
		t.Errorf("total %d != len(ListMessages) %d", total, len(list))
	}
	if unread != wantUnread {
		t.Errorf("unread %d != ListMessages unread tally %d", unread, wantUnread)
	}
}
