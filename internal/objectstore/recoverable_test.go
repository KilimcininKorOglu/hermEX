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
	info := mustAppendMessage(t, s, mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	mid := midString(uint64(info.ID))

	mustNoErr(t, "soft delete", s.SoftDeleteMessage(mapi.PrivateFIDInbox, info.UID))
	wantSoftDeleted(t, s, info.ID, mid)
	wantDumpsterItem(t, s, info.ID)

	rinfo := wantRecovered(t, s, info.ID)

	// --- soft delete again, then purge: gone for good ---
	mustNoErr(t, "soft delete", s.SoftDeleteMessage(mapi.PrivateFIDInbox, rinfo.UID))
	mustNoErr(t, "purge", s.PurgeSoftDeleted(info.ID))
	wantRows(t, s, "object rows after purge", 0, `SELECT COUNT(*) FROM messages WHERE message_id=?`, info.ID)
	if _, err := os.Stat(s.emlPath(mid)); !os.IsNotExist(err) {
		t.Errorf("eml survived purge: stat err = %v", err)
	}
	wantEq(t, "dumpster items after purge", len(mustListSoftDeleted(t, s)), 0)
}

// mustListSoftDeleted returns the Inbox dumpster listing.
func mustListSoftDeleted(t *testing.T, s *Store) []SoftDeletedMessage {
	t.Helper()
	dump, err := s.ListSoftDeleted(mapi.PrivateFIDInbox)
	mustNoErr(t, "list soft deleted", err)
	return dump
}

// wantSoftDeleted proves a soft delete hides the message from every live view
// while everything recovery needs survives.
func wantSoftDeleted(t *testing.T, s *Store, messageID int64, mid string) {
	t.Helper()
	wantEq(t, "live messages after soft delete", len(mustListMessages(t, s, mapi.PrivateFIDInbox)), 0)
	wantRows(t, s, "object rows flagged is_deleted=1", 1,
		`SELECT COUNT(*) FROM messages WHERE message_id=? AND is_deleted=1`, messageID)
	wantNotEq(t, "message properties after soft delete (must survive for recovery)",
		countRows(t, s, `SELECT COUNT(*) FROM message_properties WHERE message_id=?`, messageID), 0)
	if _, err := os.Stat(s.emlPath(mid)); err != nil {
		t.Errorf("eml dropped by soft delete (must survive for recovery): %v", err)
	}
	var idxCount int
	mustScan(t, s.idxdb.QueryRow(`SELECT COUNT(*) FROM messages WHERE message_id=?`, messageID), &idxCount)
	wantEq(t, "index rows after soft delete (a surviving row would still show in IMAP)", idxCount, 0)
}

// wantDumpsterItem proves the dumpster surfaces the item with its projections
// and a soft-delete timestamp.
func wantDumpsterItem(t *testing.T, s *Store, messageID int64) {
	t.Helper()
	dump := mustListSoftDeleted(t, s)
	if len(dump) != 1 {
		t.Fatalf("dumpster has %d items, want 1", len(dump))
	}
	wantEq(t, "dumpster item id", dump[0].MessageID, messageID)
	wantEq(t, "dumpster item subject", dump[0].Subject, "dumpster konusu")
	if dump[0].DeletedOn.IsZero() {
		t.Error("dumpster item has no PR_DELETED_ON timestamp")
	}
}

// wantRecovered recovers the message and proves it is live again, returning its
// fresh index row.
func wantRecovered(t *testing.T, s *Store, messageID int64) MessageInfo {
	t.Helper()
	rinfo, err := s.RecoverMessage(mapi.PrivateFIDInbox, messageID)
	mustNoErr(t, "recover", err)
	wantEq(t, "recovered subject", rinfo.Subject, "dumpster konusu")
	wantEq(t, "live messages after recover", len(mustListMessages(t, s, mapi.PrivateFIDInbox)), 1)
	wantRows(t, s, "object rows cleared to is_deleted=0", 1,
		`SELECT COUNT(*) FROM messages WHERE message_id=? AND is_deleted=0`, messageID)
	wantEq(t, "dumpster items after recover", len(mustListSoftDeleted(t, s)), 0)
	return rinfo
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
