package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// TestMessageUIDByID confirms an indexed mail message resolves to its IMAP UID,
// and that a calendar item created with CreateMessage — which never enters the
// index — reports ok=false (its notification id then carries no UID).
func TestMessageUIDByID(t *testing.T) {
	s := openSeededStore(t)

	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: hi\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nbody.\r\n")
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	uid, ok, err := s.MessageUIDByID(int64(mapi.PrivateFIDInbox), info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("indexed message must resolve a UID")
	}
	if uid != info.UID {
		t.Errorf("uid = %d, want %d", uid, info.UID)
	}

	// A calendar item created via CreateMessage is not indexed → no UID.
	mid, err := s.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{
		Props: mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "appt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.MessageUIDByID(int64(mapi.PrivateFIDCalendar), mid); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("a non-indexed calendar item must not resolve a UID")
	}
}
