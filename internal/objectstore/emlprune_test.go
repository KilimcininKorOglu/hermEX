package objectstore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// appendForPrune stores one plain message and returns it, with the wire copy
// already cached, which is the state every mailbox is in.
func appendForPrune(t *testing.T, s *Store) MessageInfo {
	t.Helper()
	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: prune me" +
		"\r\nDate: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nbody text\r\n")
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.emlPath(midString(uint64(info.ID)))); err != nil {
		t.Fatalf("the message is not cached, so the test would prove nothing: %v", err)
	}
	return info
}

// ageEML backdates a message's cached wire copy so the prune sees it as old,
// standing in for mail that has sat in the mailbox for months.
func ageEML(t *testing.T, s *Store, mid string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	if err := os.Chtimes(s.emlPath(mid), when, when); err != nil {
		t.Fatal(err)
	}
}

// TestPruneEMLCacheReclaimsOldCopies proves the cache can actually be reclaimed.
// The wire copy is a regenerable duplicate of every live message and roughly
// doubles what a mailbox costs on disk, but nothing evicts it while the message
// is still there, so an operator had no way to get that space back short of
// deleting mail.
func TestPruneEMLCacheReclaimsOldCopies(t *testing.T) {
	s := openSeededStore(t)
	info := appendForPrune(t, s)
	mid := midString(uint64(info.ID))
	fi, err := os.Stat(s.emlPath(mid))
	if err != nil {
		t.Fatal(err)
	}
	ageEML(t, s, mid, 90*24*time.Hour)

	removed, reclaimed, err := s.PruneEMLCache(time.Now().Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if reclaimed != fi.Size() {
		t.Errorf("reclaimed = %d bytes, want the file's %d", reclaimed, fi.Size())
	}
	if _, err := os.Stat(s.emlPath(mid)); !os.IsNotExist(err) {
		t.Error("the cached copy is still on disk, so no space was reclaimed")
	}
}

// TestPruneEMLCacheKeepsRecentCopies proves the working set survives. Pruning
// what clients are actively fetching would trade disk for a re-export on every
// read, which is the opposite of the point.
func TestPruneEMLCacheKeepsRecentCopies(t *testing.T) {
	s := openSeededStore(t)
	info := appendForPrune(t, s)

	removed, reclaimed, err := s.PruneEMLCache(time.Now().Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 || reclaimed != 0 {
		t.Errorf("removed %d copies (%d bytes); a just-written copy must be kept", removed, reclaimed)
	}
	if _, err := os.Stat(s.emlPath(midString(uint64(info.ID)))); err != nil {
		t.Errorf("the recent cached copy was dropped: %v", err)
	}
}

// TestPrunedMessageStillServesItsWireForm is what makes the prune safe to offer
// at all: the store re-synthesizes a missing copy from the stored object, so
// reclaiming the cache costs a re-export and never a message.
func TestPrunedMessageStillServesItsWireForm(t *testing.T) {
	s := openSeededStore(t)
	info := appendForPrune(t, s)
	ageEML(t, s, midString(uint64(info.ID)), 90*24*time.Hour)
	if _, _, err := s.PruneEMLCache(time.Now().Add(-30 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}

	raw, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
	if err != nil {
		t.Fatalf("a pruned message no longer serves its wire form: %v", err)
	}
	if !bytes.Contains(raw, []byte("prune me")) {
		t.Errorf("the regenerated wire form is not the stored message: %q", raw)
	}
	// The recorded size must follow the bytes now served, or RFC822.SIZE lies to
	// every IMAP client that asks.
	if got := indexSize(t, s, info.UID); got != int64(len(raw)) {
		t.Errorf("recorded size = %d, want the %d bytes served", got, len(raw))
	}
}

// TestPruneEMLCacheSkipsInFlightWrites proves a concurrent write survives. The
// cache is written temp-file-then-rename, and removing another writer's
// temporary would fail the write that owns it.
func TestPruneEMLCacheSkipsInFlightWrites(t *testing.T) {
	s := openSeededStore(t)
	tmp, err := os.CreateTemp(filepath.Join(s.dir, "eml"), ".eml-*")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	when := time.Now().Add(-90 * 24 * time.Hour)
	if err := os.Chtimes(tmp.Name(), when, when); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.PruneEMLCache(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp.Name()); err != nil {
		t.Error("the prune removed an in-flight temporary, which would fail the write that owns it")
	}
}

// TestPruneEMLCacheWithoutCacheDirectory proves a mailbox that has no cache
// directory is a no-op rather than an error: the deployment-wide sweep walks
// every account, and one never-used mailbox must not fail the run.
func TestPruneEMLCacheWithoutCacheDirectory(t *testing.T) {
	s := openSeededStore(t)
	if err := os.RemoveAll(filepath.Join(s.dir, "eml")); err != nil {
		t.Fatal(err)
	}
	removed, reclaimed, err := s.PruneEMLCache(time.Now())
	if err != nil {
		t.Fatalf("a mailbox with no cache directory reported an error: %v", err)
	}
	if removed != 0 || reclaimed != 0 {
		t.Errorf("removed = %d, reclaimed = %d, want nothing", removed, reclaimed)
	}
}
