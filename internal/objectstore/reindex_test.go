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
	eidA := mustCreateMessage(t, s, mapi.PrivateFIDInbox, msgA)
	wantEq(t, "A index rows before reindex", idxCount(t, s, eidA), 0)

	// B: a delivered message whose object we remove directly, leaving an orphan
	// index row + eml.
	rawB := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: yetim indeks\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\ngövde B.\r\n")
	infoB := mustAppendMessage(t, s, mapi.PrivateFIDInbox, rawB, time.Unix(1700000000, 0), 0)
	bEML := s.emlPath(midString(uint64(infoB.ID)))
	_, err := s.objdb.Exec(`DELETE FROM messages WHERE message_id=?`, infoB.ID)
	mustNoErr(t, "drop object B", err)

	mustNoErr(t, "reindex folder", s.ReindexFolder(mapi.PrivateFIDInbox))

	// A is now indexed; B is pruned.
	foundA := indexedAfterReindex(t, s, eidA, infoB.ID)
	if foundA.UID == 0 || foundA.Size == 0 {
		t.Errorf("A indexed with uid=%d size=%d, want both nonzero", foundA.UID, foundA.Size)
	}

	// A's eml was generated and its index size matches the served bytes.
	emlA, err := os.ReadFile(s.emlPath(midString(uint64(eidA))))
	mustNoErr(t, "read A eml after reindex", err)
	wantEq(t, "A index size against the served eml", foundA.Size, int64(len(emlA)))

	// B's orphan eml was removed.
	if _, err := os.Stat(bEML); !os.IsNotExist(err) {
		t.Errorf("B orphan eml survived prune: stat err = %v", err)
	}
}

// indexedAfterReindex returns the index row the reindex created for the orphan
// object, failing when it is missing or when the orphan index row survived.
func indexedAfterReindex(t *testing.T, s *Store, indexedID, prunedID int64) MessageInfo {
	t.Helper()
	var found *MessageInfo
	list := mustListMessages(t, s, mapi.PrivateFIDInbox)
	for i := range list {
		if list[i].ID == indexedID {
			found = &list[i]
		}
		if list[i].ID == prunedID {
			t.Error("orphan index row was not pruned")
		}
	}
	if found == nil {
		t.Fatal("orphan object was not indexed")
	}
	return *found
}

// TestReindexPreservesExistingUID checks that reindexing leaves an
// already-indexed message untouched: its UID (and thus UIDVALIDITY) survives,
// so a reindex never renumbers messages a client has already seen.
func TestReindexPreservesExistingUID(t *testing.T) {
	s := openSeededStore(t)

	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: kalıcı\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\ngövde.\r\n")
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if info.UID == 0 {
		t.Fatalf("delivered message has UID 0")
	}

	if err := s.ReindexFolder(mapi.PrivateFIDInbox); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListMessages(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	var found *MessageInfo
	for i := range list {
		if list[i].ID == info.ID {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatal("message vanished from the index after reindex")
	}
	if found.UID != info.UID {
		t.Errorf("UID changed across reindex: was %d, now %d", info.UID, found.UID)
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
