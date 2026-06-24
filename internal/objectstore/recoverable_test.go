package objectstore

import (
	"errors"
	"os"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestRecoverableItems exercises the Recoverable Items dumpster end to end: a
// soft-delete hides the message from every live view yet keeps it recoverable
// (object row, properties, and eml survive, flagged is_deleted=1); ListSoftDeleted
// surfaces it; RecoverMessage brings it back into the folder with a live index
// row; and PurgeSoftDeleted removes it for good. This proves delete-to-dumpster is
// reversible, which is the whole point of the feature, not just that a flag flips.
func TestRecoverableItems(t *testing.T) {
	s := openSeededStore(t)

	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: dumpster konusu\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\ngövde metni.\r\n")
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	mid := midString(uint64(info.ID))

	// --- soft delete: vanishes from the live view, survives in the store ---
	if err := s.SoftDeleteMessage(mapi.PrivateFIDInbox, info.UID); err != nil {
		t.Fatal(err)
	}
	live, err := s.ListMessages(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Errorf("soft-deleted message still in live list: got %d, want 0", len(live))
	}
	if countRows(t, s, `SELECT COUNT(*) FROM messages WHERE message_id=? AND is_deleted=1`, info.ID) != 1 {
		t.Error("object row not flagged is_deleted=1 after soft delete")
	}
	if countRows(t, s, `SELECT COUNT(*) FROM message_properties WHERE message_id=?`, info.ID) == 0 {
		t.Error("message properties dropped by soft delete (must survive for recovery)")
	}
	if _, err := os.Stat(s.emlPath(mid)); err != nil {
		t.Errorf("eml dropped by soft delete (must survive for recovery): %v", err)
	}
	var idxCount int
	if err := s.idxdb.QueryRow(`SELECT COUNT(*) FROM messages WHERE message_id=?`, info.ID).Scan(&idxCount); err != nil {
		t.Fatal(err)
	}
	if idxCount != 0 {
		t.Error("index row survived soft delete (message would still show in IMAP/lists)")
	}

	// --- dumpster lists it with its projections and a soft-delete timestamp ---
	dump, err := s.ListSoftDeleted(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(dump) != 1 {
		t.Fatalf("dumpster has %d items, want 1", len(dump))
	}
	if dump[0].MessageID != info.ID {
		t.Errorf("dumpster item id = %d, want %d", dump[0].MessageID, info.ID)
	}
	if dump[0].Subject != "dumpster konusu" {
		t.Errorf("dumpster item subject = %q, want %q", dump[0].Subject, "dumpster konusu")
	}
	if dump[0].DeletedOn.IsZero() {
		t.Error("dumpster item has no PR_DELETED_ON timestamp")
	}

	// --- recover: back in the folder with a fresh live index row ---
	rinfo, err := s.RecoverMessage(mapi.PrivateFIDInbox, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rinfo.Subject != "dumpster konusu" {
		t.Errorf("recovered subject = %q, want %q", rinfo.Subject, "dumpster konusu")
	}
	live, err = s.ListMessages(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Fatalf("recovered folder lists %d messages, want 1", len(live))
	}
	if countRows(t, s, `SELECT COUNT(*) FROM messages WHERE message_id=? AND is_deleted=0`, info.ID) != 1 {
		t.Error("object row not cleared to is_deleted=0 after recover")
	}
	if dump, _ := s.ListSoftDeleted(mapi.PrivateFIDInbox); len(dump) != 0 {
		t.Errorf("dumpster still has %d items after recover, want 0", len(dump))
	}

	// --- soft delete again, then purge: gone for good ---
	if err := s.SoftDeleteMessage(mapi.PrivateFIDInbox, rinfo.UID); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeSoftDeleted(info.ID); err != nil {
		t.Fatal(err)
	}
	if countRows(t, s, `SELECT COUNT(*) FROM messages WHERE message_id=?`, info.ID) != 0 {
		t.Error("object row survived purge")
	}
	if _, err := os.Stat(s.emlPath(mid)); !os.IsNotExist(err) {
		t.Errorf("eml survived purge: stat err = %v", err)
	}
	if dump, _ := s.ListSoftDeleted(mapi.PrivateFIDInbox); len(dump) != 0 {
		t.Errorf("dumpster has %d items after purge, want 0", len(dump))
	}
}

// TestPurgeSoftDeletedRefusesLiveMessage proves the explicit dumpster purge cannot
// destroy a live message: only an is_deleted=1 item is purgeable through it.
func TestPurgeSoftDeletedRefusesLiveMessage(t *testing.T) {
	s := openSeededStore(t)
	raw := []byte("From: a@example.test\r\nSubject: canlı\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nx\r\n")
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeSoftDeleted(info.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("purge of live message err = %v, want ErrNotFound", err)
	}
	if countRows(t, s, `SELECT COUNT(*) FROM messages WHERE message_id=?`, info.ID) != 1 {
		t.Error("live message was purged through the dumpster path")
	}
}

// TestPurgeSoftDeletedOlderThan proves the retention sweep purges items aged past
// the cutoff and keeps fresher ones, by their PR_DELETED_ON stamp.
func TestPurgeSoftDeletedOlderThan(t *testing.T) {
	s := openSeededStore(t)
	raw := []byte("From: a@example.test\r\nSubject: eski\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nx\r\n")
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SoftDeleteMessage(mapi.PrivateFIDInbox, info.UID); err != nil {
		t.Fatal(err)
	}

	// Cutoff in the past: the just-deleted item is newer, so it is kept.
	n, err := s.PurgeSoftDeletedOlderThan(time.Now().Add(-1 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("purged %d with a past cutoff, want 0", n)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM messages WHERE message_id=?`, info.ID); got != 1 {
		t.Errorf("item gone after sub-cutoff sweep: rows=%d", got)
	}

	// Cutoff in the future: the item is older than it, so it is purged.
	n, err = s.PurgeSoftDeletedOlderThan(time.Now().Add(1 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("purged %d with a future cutoff, want 1", n)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM messages WHERE message_id=?`, info.ID); got != 0 {
		t.Errorf("item survived retention purge: rows=%d", got)
	}
}
