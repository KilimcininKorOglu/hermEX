package relay

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openSpool(t *testing.T) *Spool {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestSpoolBaselineAdoption proves an existing, unversioned spool, tables present
// and user_version 0, as written before the spool was versioned, is adopted as
// v1 on open without disturbing its queued data.
func TestSpoolBaselineAdoption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.sqlite3")

	// Create the spool the pre-migration way: raw tables, no version stamp.
	raw, err := sql.Open("sqlite", dsn(path))
	mustNoErr(t, err, "open the raw database")
	for _, stmt := range []string{
		`CREATE TABLE messages (id INTEGER PRIMARY KEY, envelope_from TEXT NOT NULL, body BLOB NOT NULL, enqueued_at INTEGER NOT NULL)`,
		`CREATE TABLE recipients (id INTEGER PRIMARY KEY, message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE, recipient TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL, last_error TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX recipients_due ON recipients(next_attempt)`,
		`INSERT INTO messages (envelope_from, body, enqueued_at) VALUES ('a@x.test', 'hi', 1)`,
	} {
		_, err := raw.Exec(stmt)
		mustNoErr(t, err, "seed the pre-migration spool")
	}
	var v int
	mustNoErr(t, raw.QueryRow("PRAGMA user_version").Scan(&v), "read the seeded user_version")
	wantEq(t, v, 0, "the seeded user_version")
	raw.Close()

	// Opening through the spool adopts the baseline and records the version.
	s, err := Open(path)
	mustNoErr(t, err, "open the existing spool")
	defer s.Close()
	wantEq(t, mustCount(t, s, "PRAGMA user_version"), newestSpoolVersion(), "user_version after adoption")
	wantEq(t, mustCount(t, s, "SELECT COUNT(*) FROM messages"), 1,
		"messages after adoption (adoption must not disturb data)")
}

// newestSpoolVersion is the version a freshly migrated spool ends at: adoption
// records the baseline, then every pending migration applies in turn. It is
// computed from the migration set rather than hardcoded, so a new migration does
// not require editing an assertion to keep it passing.
func newestSpoolVersion() int {
	newest := 0
	for _, m := range spoolMigrations {
		if m.Version > newest {
			newest = m.Version
		}
	}
	return newest
}

// TestSpoolListRetryDelete proves the administrative mail-queue projection and
// actions: List reports every queued recipient with its message metadata, RetryNow
// makes a deferred recipient immediately claimable without losing its history, and
// Delete drops a recipient (and the body once none remain) without a bounce.
func TestSpoolListRetryDelete(t *testing.T) {
	s := openSpool(t)
	t0 := time.Unix(2_000_000, 0)
	body := []byte("From: a@local\r\nSubject: hi\r\n\r\nbody\r\n")
	mustNoErr(t, s.Enqueue("a@local", []string{"x@remote", "y@remote"}, body, t0), "enqueue")

	// Defer x far into the future with a recorded error (a transient failure).
	due, _ := s.Claim(t0, 10)
	xID, yID := recipientIDs(due)
	future := t0.Add(time.Hour)
	mustNoErr(t, s.Retry(xID, future, "451 greylisted"), "defer x")

	checkQueueEntry(t, s, xID, body, future)
	checkRetryNow(t, s, xID, yID, t0)
	checkQueueDelete(t, s, xID, yID)
}

// recipientIDs maps the two seeded recipients to their spool ids.
func recipientIDs(items []Item) (xID, yID int64) {
	for _, it := range items {
		if it.Recipient == "x@remote" {
			xID = it.RecipientID
			continue
		}
		yID = it.RecipientID
	}
	return xID, yID
}

// checkQueueEntry asserts the administrative projection of a deferred recipient.
func checkQueueEntry(t *testing.T, s *Spool, xID int64, body []byte, future time.Time) {
	t.Helper()
	list, err := s.List()
	mustNoErr(t, err, "list the queue")
	if len(list) != 2 {
		t.Fatalf("List returned %d entries, want 2", len(list))
	}
	var x QueueEntry
	for _, e := range list {
		if e.RecipientID == xID {
			x = e
		}
	}
	wantEq(t, x.From, "a@local", "entry envelope from")
	wantEq(t, x.Recipient, "x@remote", "entry recipient")
	wantEq(t, x.Attempts, 1, "entry attempt count")
	wantEq(t, x.LastError, "451 greylisted", "entry last error")
	wantEq(t, x.Size, len(body), "entry size")
	wantTrue(t, x.NextAttempt.Equal(future.UTC()), "entry next-attempt is the deferred time")
}

// checkRetryNow asserts that a not-yet-due recipient becomes claimable at once,
// keeping its history.
func checkRetryNow(t *testing.T, s *Spool, xID, yID int64, t0 time.Time) {
	t.Helper()
	before, _ := s.Claim(t0, 10)
	if len(before) != 1 {
		t.Fatalf("before flush %d items are due, want only y", len(before))
	}
	wantEq(t, before[0].RecipientID, yID, "the only due recipient before the flush")

	mustNoErr(t, s.RetryNow(xID, t0), "retry-now")
	after, _ := s.Claim(t0, 10)
	if len(after) != 2 {
		t.Fatalf("after flush %d items are due, want both", len(after))
	}
	for _, it := range after {
		if it.RecipientID == xID {
			wantEq(t, it.Attempts, 1, "the attempt count the flush kept")
		}
	}
}

// checkQueueDelete asserts a recipient can be dropped without a bounce, that the
// shared body survives until the last one is gone, and that a repeat delete is a
// no-op.
func checkQueueDelete(t *testing.T, s *Spool, xID, yID int64) {
	t.Helper()
	mustNoErr(t, s.Delete(xID), "delete x")
	list, _ := s.List()
	if len(list) != 1 {
		t.Fatalf("after deleting x the queue holds %d entries, want only y", len(list))
	}
	wantEq(t, list[0].RecipientID, yID, "the remaining recipient")

	mustNoErr(t, s.Delete(yID), "delete y")
	list, _ = s.List()
	wantEq(t, len(list), 0, "queue entries after deleting every recipient")

	mustNoErr(t, s.Delete(xID), "delete an already-gone recipient")
}

// TestSpoolPerRecipientLifecycle proves the core durability contract: a
// submission to several externals is queued per recipient, a delivered recipient
// is settled without disturbing the others (the shared body survives until the
// last is gone), and a transient failure defers only that recipient.
func TestSpoolPerRecipientLifecycle(t *testing.T) {
	s := openSpool(t)
	t0 := time.Unix(1_000_000, 0)
	body := []byte("From: a@local\r\nSubject: hi\r\n\r\nbody\r\n")

	mustNoErr(t, s.Enqueue("a@local", []string{"x@remote", "y@remote"}, body, t0), "enqueue")

	due, err := s.Claim(t0, 10)
	mustNoErr(t, err, "claim")
	if len(due) != 2 {
		t.Fatalf("claimed %d items, want 2 (one per recipient)", len(due))
	}
	byRcpt := map[string]Item{}
	for _, it := range due {
		wantEq(t, it.From, "a@local", "item envelope from")
		wantTrue(t, bytes.Equal(it.Body, body), "the body of the item for "+it.Recipient)
		wantEq(t, it.Attempts, 0, "a fresh item's attempt count")
		byRcpt[it.Recipient] = it
	}

	// Settle x as sent. y must remain claimable with the body intact, the shared
	// message row may not be dropped while a recipient still references it.
	mustNoErr(t, s.Sent(byRcpt["x@remote"].RecipientID), "settle x as sent")
	after, err := s.Claim(t0, 10)
	mustNoErr(t, err, "claim after settling x")
	if len(after) != 1 {
		t.Fatalf("after settling x, %d items are claimable, want only y@remote", len(after))
	}
	wantEq(t, after[0].Recipient, "y@remote", "the remaining claimable recipient")
	wantTrue(t, bytes.Equal(after[0].Body, body), "the body after a sibling recipient was settled")

	// Defer y by an hour: it must drop out of the now-claim and reappear later
	// with an incremented attempt count.
	mustNoErr(t, s.Retry(after[0].RecipientID, t0.Add(time.Hour), "452 try later"), "defer y")
	now, _ := s.Claim(t0, 10)
	wantEq(t, len(now), 0, "items claimable now after y was deferred")

	later, err := s.Claim(t0.Add(time.Hour), 10)
	mustNoErr(t, err, "claim an hour later")
	if len(later) != 1 {
		t.Fatalf("an hour later %d items are claimable, want one", len(later))
	}
	wantEq(t, later[0].Attempts, 1, "the deferred item's attempt count")

	// Settling the last recipient drops the message body too.
	mustNoErr(t, s.Sent(later[0].RecipientID), "settle y as sent")
	final, _ := s.Claim(t0.Add(2*time.Hour), 10)
	wantEq(t, len(final), 0, "items left after every recipient settled")
}

// TestSpoolDurableAcrossReopen proves the spool survives a process restart: a
// message enqueued, then the handle closed and reopened, is still claimable.
func TestSpoolDurableAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.sqlite3")
	t0 := time.Unix(2_000_000, 0)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Enqueue("a@local", []string{"x@remote"}, []byte("raw"), t0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	due, err := reopened.Claim(t0, 10)
	if err != nil {
		t.Fatalf("claim after reopen: %v", err)
	}
	if len(due) != 1 || due[0].Recipient != "x@remote" {
		t.Fatalf("after reopen, claim = %v, want the persisted x@remote", due)
	}
}

// TestSpoolDSNNotifyRoundTrip proves a recipient's RFC 3461 NOTIFY survives the
// EnqueueDSN to Claim round trip (the value the give-up path reads to decide
// whether a bounce is wanted), and that the plain Enqueue path leaves it empty
// (the "send failure DSN" default). A silent loss here would let a NEVER
// recipient receive backscatter, so the assertion is on the exact value.
func TestSpoolDSNNotifyRoundTrip(t *testing.T) {
	s := openSpool(t)
	t0 := time.Unix(3_000_000, 0)
	body := []byte("Subject: hi\r\n\r\nbody\r\n")

	// One DSN-carrying recipient (NOTIFY=NEVER, with ORCPT) and, via plain
	// Enqueue, one with no DSN preference.
	if err := s.EnqueueDSN("a@local", "HDRS", "envid-1",
		[]DSNRecipient{{Addr: "never@remote", Notify: "NEVER", ORCPT: "rfc822;never@remote"}},
		body, t0); err != nil {
		t.Fatalf("enqueue-dsn: %v", err)
	}
	if err := s.Enqueue("a@local", []string{"plain@remote"}, body, t0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	due, err := s.Claim(t0, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	got := map[string]string{}
	for _, it := range due {
		got[it.Recipient] = it.Notify
	}
	if v, ok := got["never@remote"]; !ok || v != "NEVER" {
		t.Errorf("claimed NOTIFY for never@remote = %q (present %v), want \"NEVER\"", v, ok)
	}
	if v, ok := got["plain@remote"]; !ok || v != "" {
		t.Errorf("claimed NOTIFY for plain@remote = %q (present %v), want empty", v, ok)
	}
}

// TestSpoolBackupSnapshotsQueuedMail proves the spool backup produces an openable
// copy that still holds the accepted-but-undelivered outbound mail, so a data_dir
// loss followed by a restore does not silently drop mail senders were told was
// accepted.
func TestSpoolBackupSnapshotsQueuedMail(t *testing.T) {
	sp := openSpool(t)
	t0 := time.Unix(3_000_000, 0)
	if err := sp.Enqueue("alice@local", []string{"bob@remote"},
		[]byte("From: alice@local\r\nSubject: queued\r\n\r\nhi\r\n"), t0); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "relay-copy.sqlite3")
	if err := sp.Backup(dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	// A re-run replaces rather than fails.
	if err := sp.Backup(dest); err != nil {
		t.Fatalf("Backup re-run: %v", err)
	}

	copySpool, err := Open(dest)
	if err != nil {
		t.Fatalf("the backup does not open as a spool: %v", err)
	}
	defer copySpool.Close()
	queued, err := copySpool.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].Recipient != "bob@remote" {
		t.Fatalf("backup holds %+v, want the one queued recipient bob@remote", queued)
	}
}
