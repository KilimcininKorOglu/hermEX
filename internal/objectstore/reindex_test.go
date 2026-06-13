package objectstore

import (
	"os"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// TestReindexFolder exercises both repairs: an object message with no index row
// (crash gap) gets indexed with a fresh UID and a re-synthesized eml, and an
// index row whose object is gone (interrupted delete) is pruned.
func TestReindexFolder(t *testing.T) {
	s := openSeededStore(t)

	// A: object-only message (CreateMessage never indexes), so no index row yet.
	msgA := &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "yetim nesne"},
		{Tag: mapi.PrBody, Value: "gövde A"},
		{Tag: mapi.PrMessageDeliveryTime, Value: mapi.UnixToNTTime(time.Unix(1700000000, 0))},
	}}
	eidA, err := s.CreateMessage(mapi.PrivateFIDInbox, msgA)
	if err != nil {
		t.Fatal(err)
	}
	if n := idxCount(t, s, eidA); n != 0 {
		t.Fatalf("A has %d index rows before reindex, want 0", n)
	}

	// B: a delivered message whose object we remove directly, leaving an orphan
	// index row + eml.
	rawB := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: yetim indeks\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\ngövde B.\r\n")
	infoB, err := s.AppendMessage(mapi.PrivateFIDInbox, rawB, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	bEML := s.emlPath(midString(uint64(infoB.ID)))
	if _, err := s.objdb.Exec(`DELETE FROM messages WHERE message_id=?`, infoB.ID); err != nil {
		t.Fatal(err)
	}

	if err := s.ReindexFolder(mapi.PrivateFIDInbox); err != nil {
		t.Fatal(err)
	}

	// A is now indexed; B is pruned.
	list, err := s.ListMessages(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	var foundA *MessageInfo
	for i := range list {
		if list[i].ID == eidA {
			foundA = &list[i]
		}
		if list[i].ID == infoB.ID {
			t.Error("orphan index row B was not pruned")
		}
	}
	if foundA == nil {
		t.Fatal("orphan object A was not indexed")
	}
	if foundA.UID == 0 || foundA.Size == 0 {
		t.Errorf("A indexed with uid=%d size=%d, want both nonzero", foundA.UID, foundA.Size)
	}

	// A's eml was generated and its index size matches the served bytes.
	emlA, err := os.ReadFile(s.emlPath(midString(uint64(eidA))))
	if err != nil {
		t.Fatalf("A eml missing after reindex: %v", err)
	}
	if int64(len(emlA)) != foundA.Size {
		t.Errorf("A index size %d != served eml %d", foundA.Size, len(emlA))
	}

	// B's orphan eml was removed.
	if _, err := os.Stat(bEML); !os.IsNotExist(err) {
		t.Errorf("B orphan eml survived prune: stat err = %v", err)
	}
}

// idxCount returns how many index rows reference a message id.
func idxCount(t *testing.T, s *Store, messageID int64) int {
	t.Helper()
	var n int
	if err := s.idxdb.QueryRow(`SELECT COUNT(*) FROM messages WHERE message_id=?`, messageID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
