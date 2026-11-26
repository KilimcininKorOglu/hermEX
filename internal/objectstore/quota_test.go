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
	size1 := mustMailboxSize(t, s)
	if size1 <= 0 {
		t.Fatalf("after one message size = %d, want > 0", size1)
	}
	if _, err := s.AppendMessage(inbox, raw2, when, 0); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	size2 := mustMailboxSize(t, s)
	if size2 <= size1 {
		t.Fatalf("after two messages size = %d, want > %d", size2, size1)
	}

	if err := s.DeleteMessage(inbox, info1.UID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	size3 := mustMailboxSize(t, s)
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
	mustAppendMessage(t, s, inbox, live, when, 0)
	liveSize := mustMailboxSize(t, s)
	if liveSize <= 0 {
		t.Fatalf("live-only size = %d, want > 0", liveSize)
	}

	doomed := []byte("From: b@hermex.test\r\nTo: u@hermex.test\r\nSubject: trash\r\n\r\nthis message is soft-deleted into the dumpster and must not be charged to quota\r\n")
	info := mustAppendMessage(t, s, inbox, doomed, when, 0)
	if withDoomed := mustMailboxSize(t, s); withDoomed <= liveSize {
		t.Fatalf("doomed message added no bytes (with=%d, live=%d); the test would be vacuous", withDoomed, liveSize)
	}

	// Soft-delete sends it to the dumpster (is_deleted=1), not a purge.
	mustNoErr(t, "soft-delete", s.SoftDeleteMessage(inbox, info.UID))

	// It survives in the dumpster, still recoverable...
	dump, err := s.ListAllSoftDeleted()
	mustNoErr(t, "list soft-deleted", err)
	if len(dump) != 1 {
		t.Fatalf("dumpster = %d, want 1 (the message must still exist, just soft-deleted)", len(dump))
	}

	// ...yet its bytes no longer count toward quota usage.
	wantEq(t, "MailboxSize after soft-delete (soft-deleted bytes are excluded from quota)",
		mustMailboxSize(t, s), liveSize)
}

// TestOverReceiveQuota pins the receive-quota rule every write path shares: a
// zero limit never trips, a mailbox at or under its limit is not over, and one
// whose used bytes pass the limit is. The limit is in KiB, so the boundary is
// limit*1024 bytes.
func TestOverReceiveQuota(t *testing.T) {
	s := openSeededStore(t)
	raw := []byte("From: a@hermex.test\r\nTo: u@hermex.test\r\nSubject: q\r\n\r\n" +
		string(make([]byte, 2048)) + "\r\n")
	mustAppendMessage(t, s, int64(mapi.PrivateFIDInbox), raw, time.Unix(1700000000, 0), 0)
	used := mustMailboxSize(t, s)
	if used <= 1024 || used > 3*1024 {
		t.Fatalf("seeded usage %d bytes falls outside the 1-3 KiB the limits below assume", used)
	}

	for _, c := range []struct {
		limitKB uint32
		want    bool
	}{
		{0, false}, // unlimited
		{1, true},  // 1 KiB, usage is over it
		{3, false}, // 3 KiB, usage is under it
	} {
		mustNoErr(t, "set quota", s.SetQuota(QuotaLimits{ReceiveKB: c.limitKB}))
		over, err := s.OverReceiveQuota()
		mustNoErr(t, "over receive quota", err)
		if over != c.want {
			t.Errorf("ReceiveKB=%d with %d bytes used: over = %v, want %v", c.limitKB, used, over, c.want)
		}
	}
}

// mustMailboxSize returns the mailbox usage the quota path reports.
func mustMailboxSize(t *testing.T, s *Store) int64 {
	t.Helper()
	size, err := s.MailboxSize()
	mustNoErr(t, "mailbox size", err)
	return size
}
