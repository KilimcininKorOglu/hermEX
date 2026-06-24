package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestQuotaRoundTrip proves the store quota limits persist and an unset store
// reads as unlimited (all zero).
func TestQuotaRoundTrip(t *testing.T) {
	s := openSeededStore(t)

	got, err := s.GetQuota()
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if got != (QuotaLimits{}) {
		t.Errorf("fresh store quota = %+v, want all zero (unlimited)", got)
	}

	want := QuotaLimits{SendKB: 1024, ReceiveKB: 2048, StorageKB: 4096}
	if err := s.SetQuota(want); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	got, err = s.GetQuota()
	if err != nil {
		t.Fatalf("get quota after set: %v", err)
	}
	if got != want {
		t.Errorf("quota = %+v, want %+v", got, want)
	}
}

// TestMailboxSize proves the used space sums the stored message sizes and
// excludes deleted messages, so deleting a message frees quota.
func TestMailboxSize(t *testing.T) {
	s := openSeededStore(t)
	when := time.Unix(1700000000, 0)
	inbox := int64(mapi.PrivateFIDInbox)

	if size, err := s.MailboxSize(); err != nil || size != 0 {
		t.Fatalf("empty mailbox size = (%d,%v), want 0", size, err)
	}

	raw1 := []byte("From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: one\r\n\r\nfirst message body\r\n")
	raw2 := []byte("From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: two\r\n\r\nsecond message, a little longer than the first\r\n")

	info1, err := s.AppendMessage(inbox, raw1, when, 0)
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	size1, _ := s.MailboxSize()
	if size1 <= 0 {
		t.Fatalf("after one message size = %d, want > 0", size1)
	}
	if _, err := s.AppendMessage(inbox, raw2, when, 0); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	size2, _ := s.MailboxSize()
	if size2 <= size1 {
		t.Fatalf("after two messages size = %d, want > %d", size2, size1)
	}

	if err := s.DeleteMessage(inbox, info1.UID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	size3, _ := s.MailboxSize()
	if size3 != size2-size1 {
		t.Errorf("after delete size = %d, want %d (second message only)", size3, size2-size1)
	}
}

// TestMailboxSizeExcludesSoftDeleted proves a soft-deleted message (one in the
// Recoverable Items dumpster, is_deleted=1, still recoverable until retention)
// does not count toward the mailbox usage MailboxSize reports: quota is charged on
// live mail only. Unlike a hard delete this leaves the row in place, so the message
// is asserted still present in the dumpster yet absent from the usage total. The
// test fails if the is_deleted filter were dropped from the quota sum.
func TestMailboxSizeExcludesSoftDeleted(t *testing.T) {
	s := openSeededStore(t)
	when := time.Unix(1700000000, 0)
	inbox := int64(mapi.PrivateFIDInbox)

	live := []byte("From: a@hermex.test\r\nTo: u@hermex.test\r\nSubject: keep\r\n\r\nlive message body that counts toward quota\r\n")
	if _, err := s.AppendMessage(inbox, live, when, 0); err != nil {
		t.Fatalf("append live: %v", err)
	}
	liveSize, err := s.MailboxSize()
	if err != nil {
		t.Fatalf("mailbox size: %v", err)
	}
	if liveSize <= 0 {
		t.Fatalf("live-only size = %d, want > 0", liveSize)
	}

	doomed := []byte("From: b@hermex.test\r\nTo: u@hermex.test\r\nSubject: trash\r\n\r\nthis message is soft-deleted into the dumpster and must not be charged to quota\r\n")
	info, err := s.AppendMessage(inbox, doomed, when, 0)
	if err != nil {
		t.Fatalf("append doomed: %v", err)
	}
	withDoomed, err := s.MailboxSize()
	if err != nil {
		t.Fatalf("mailbox size: %v", err)
	}
	if withDoomed <= liveSize {
		t.Fatalf("doomed message added no bytes (with=%d, live=%d); the test would be vacuous", withDoomed, liveSize)
	}

	// Soft-delete sends it to the dumpster (is_deleted=1), not a purge.
	if err := s.SoftDeleteMessage(inbox, info.UID); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}

	// It survives in the dumpster, still recoverable...
	dump, err := s.ListAllSoftDeleted()
	if err != nil {
		t.Fatalf("list soft-deleted: %v", err)
	}
	if len(dump) != 1 {
		t.Fatalf("dumpster = %d, want 1 (the message must still exist, just soft-deleted)", len(dump))
	}

	// ...yet its bytes no longer count toward quota usage.
	got, err := s.MailboxSize()
	if err != nil {
		t.Fatalf("mailbox size: %v", err)
	}
	if got != liveSize {
		t.Errorf("MailboxSize after soft-delete = %d, want %d (soft-deleted bytes must be excluded from quota)", got, liveSize)
	}
}
