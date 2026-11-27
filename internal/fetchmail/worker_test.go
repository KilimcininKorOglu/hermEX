package fetchmail

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
)

// fakeWorkerStore supplies poll configurations and an in-memory POP3 seen-set.
type fakeWorkerStore struct {
	configs []directory.FetchmailEntry
	seen    map[int64]map[string]bool
	marked  map[int64][]string
}

func (f *fakeWorkerStore) ListActiveFetchmail() ([]directory.FetchmailEntry, error) {
	return f.configs, nil
}
func (f *fakeWorkerStore) FetchmailSeen(id int64) (map[string]bool, error) { return f.seen[id], nil }
func (f *fakeWorkerStore) MarkFetchmailSeen(id int64, uids []string) error {
	if f.marked == nil {
		f.marked = map[int64][]string{}
	}
	f.marked[id] = append(f.marked[id], uids...)
	return nil
}

var pollTime = time.Unix(1700000000, 0)

// capture collects delivered messages.
func capture() (Deliverer, *[]string) {
	var got []string
	return func(_ string, raw []byte, _ time.Time) error {
		got = append(got, string(raw))
		return nil
	}, &got
}

// failSecond is a deliverer whose second delivery fails.
func failSecond() Deliverer {
	calls := 0
	return func(_ string, _ []byte, _ time.Time) error {
		calls++
		if calls == 2 {
			return fmt.Errorf("delivery failed")
		}
		return nil
	}
}

// pop3Entry is a POP3 poll configuration for src.
func pop3Entry(src *pop3Source, keep bool) directory.FetchmailEntry {
	return directory.FetchmailEntry{
		ID: 1, Mailbox: "alice@hermex.test", Active: true,
		SrcServer: src.host, SrcPort: src.port, SrcUser: "alice", SrcPassword: "pw",
		Protocol: "POP3", Keep: keep,
	}
}

// imapEntry is an IMAP poll configuration for src.
func imapEntry(src *imapSource, keep bool) directory.FetchmailEntry {
	return directory.FetchmailEntry{
		ID: 2, Mailbox: "alice@hermex.test", Active: true,
		SrcServer: src.host, SrcPort: src.port, SrcUser: "alice", SrcPassword: "pw",
		Protocol: "IMAP", SrcFolder: "INBOX", Keep: keep,
	}
}

// TestPollPOP3DeleteMode proves a non-kept POP3 account delivers every message and deletes
// each from the source (so the source itself prevents a re-fetch).
func TestPollPOP3DeleteMode(t *testing.T) {
	allowLoopback(t)
	msgs := []string{"From: a\r\n\r\none", "From: b\r\n\r\ntwo"}
	src := fakePOP3(t, msgs)
	store := &fakeWorkerStore{configs: []directory.FetchmailEntry{pop3Entry(src, false)}}
	deliver, got := capture()

	n, errs := Poll(context.Background(), store, deliver, pollTime)
	if len(errs) != 0 {
		t.Fatalf("poll errors: %v", errs)
	}
	if n != 2 || len(*got) != 2 || (*got)[0] != msgs[0]+"\r\n" {
		t.Errorf("delivered %d %q, want both messages", n, *got)
	}
	if deleted := src.deleted(); len(deleted) != 2 {
		t.Errorf("server deletions = %v, want both messages deleted", deleted)
	}
}

// TestPollPOP3KeepDedup proves a kept POP3 account skips an already-seen id, delivers only
// the new message, records its id, and deletes nothing on the source.
func TestPollPOP3KeepDedup(t *testing.T) {
	allowLoopback(t)
	msgs := []string{"From: a\r\n\r\none", "From: b\r\n\r\ntwo"}
	src := fakePOP3(t, msgs)
	store := &fakeWorkerStore{
		configs: []directory.FetchmailEntry{pop3Entry(src, true)},
		seen:    map[int64]map[string]bool{1: {"uid1": true}},
	}
	deliver, got := capture()

	n, errs := Poll(context.Background(), store, deliver, pollTime)
	if len(errs) != 0 {
		t.Fatalf("poll errors: %v", errs)
	}
	if n != 1 || len(*got) != 1 || (*got)[0] != msgs[1]+"\r\n" {
		t.Errorf("delivered %d %q, want only the unseen message", n, *got)
	}
	if want := []string{"uid2"}; len(store.marked[1]) != 1 || store.marked[1][0] != want[0] {
		t.Errorf("recorded seen = %v, want %v", store.marked[1], want)
	}
	if deleted := src.deleted(); len(deleted) != 0 {
		t.Errorf("a kept account deleted from the source: %v", deleted)
	}
}

// TestPollPOP3KeepRecordsBeforeLaterFailure proves a kept POP3 account records each id as it
// is delivered, not after the whole batch: when delivery fails on a later message, the ones
// already delivered stay recorded so the next poll does not re-deliver them.
func TestPollPOP3KeepRecordsBeforeLaterFailure(t *testing.T) {
	allowLoopback(t)
	src := fakePOP3(t, []string{"From: a\r\n\r\none", "From: b\r\n\r\ntwo"})
	store := &fakeWorkerStore{configs: []directory.FetchmailEntry{pop3Entry(src, true)}}

	n, errs := Poll(context.Background(), store, failSecond(), pollTime)
	if len(errs) != 1 {
		t.Fatalf("poll errors = %v, want the later delivery failure reported once", errs)
	}
	if n != 1 {
		t.Errorf("delivered %d, want 1 before the failure", n)
	}
	if got := store.marked[1]; len(got) != 1 || got[0] != "uid1" {
		t.Errorf("recorded seen = %v, want [uid1]: the delivered message must survive the failure", got)
	}
	if deleted := src.deleted(); len(deleted) != 0 {
		t.Errorf("a kept account deleted from the source: %v", deleted)
	}
}

// TestPollDeleteModeKeepsAMessageItCouldNotDeliver fails delivery of the second
// message on a non-kept account. Deleting it from the source anyway would lose
// it, since the local copy was never made: only the delivered message may be
// deleted, on POP3 and on IMAP.
func TestPollDeleteModeKeepsAMessageItCouldNotDeliver(t *testing.T) {
	allowLoopback(t)

	src := fakePOP3(t, []string{"From: a\r\n\r\none", "From: b\r\n\r\ntwo"})
	store := &fakeWorkerStore{configs: []directory.FetchmailEntry{pop3Entry(src, false)}}
	n, errs := Poll(context.Background(), store, failSecond(), pollTime)
	wantEq(t, n, 1, "POP3 messages delivered before the failure")
	wantEq(t, len(errs), 1, "POP3 poll errors")
	if deleted := src.deleted(); len(deleted) != 1 || deleted[0] != 1 {
		t.Errorf("POP3 deletions = %v, want only message 1", deleted)
	}

	isrc := fakeIMAP(t, "From: a\r\n\r\nbody")
	store = &fakeWorkerStore{configs: []directory.FetchmailEntry{imapEntry(isrc, false)}}
	n, errs = Poll(context.Background(), store, failSecond(), pollTime)
	wantEq(t, n, 1, "IMAP messages delivered before the failure")
	wantEq(t, len(errs), 1, "IMAP poll errors")
	if joined := strings.Join(isrc.recorded(), " | "); strings.Contains(joined, `102 +FLAGS (\Deleted)`) {
		t.Errorf("IMAP deleted the undelivered message: %q", joined)
	}
}

// TestPollReportsAFailedPOP3Quit proves a refused QUIT is not lost. RFC 1939 6
// applies the deletions only when QUIT succeeds, so a source that answers -ERR
// keeps every message the account deleted, and the next poll fetches them again.
// The poll must report that failure instead of reading as a clean run.
func TestPollReportsAFailedPOP3Quit(t *testing.T) {
	allowLoopback(t)
	src := startPOP3Source(t, []string{"From: a\r\n\r\none", "From: b\r\n\r\ntwo"}, "-ERR maildrop locked\r\n")
	store := &fakeWorkerStore{configs: []directory.FetchmailEntry{pop3Entry(src, false)}}
	deliver, got := capture()

	n, errs := Poll(context.Background(), store, deliver, pollTime)
	wantEq(t, n, 2, "messages delivered before the QUIT")
	wantEq(t, len(*got), 2, "messages filed locally")
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "quit") || !strings.Contains(errs[0].Error(), "maildrop locked") {
		t.Errorf("poll errors = %v, want the refused QUIT reported", errs)
	}
}

// TestPollReportsAFailedIMAPLogout proves a refused LOGOUT is reported the same
// way, so the operator sees a source that did not close the session cleanly.
func TestPollReportsAFailedIMAPLogout(t *testing.T) {
	allowLoopback(t)
	src := startIMAPSource(t, "From: a\r\n\r\nbody", "NO logout refused")
	store := &fakeWorkerStore{configs: []directory.FetchmailEntry{imapEntry(src, true)}}
	deliver, got := capture()

	n, errs := Poll(context.Background(), store, deliver, pollTime)
	wantEq(t, n, 2, "messages delivered before the LOGOUT")
	wantEq(t, len(*got), 2, "messages filed locally")
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "logout") || !strings.Contains(errs[0].Error(), "logout refused") {
		t.Errorf("poll errors = %v, want the refused LOGOUT reported", errs)
	}
}

// TestPollIMAPDeleteMode proves a non-kept IMAP account delivers and deletes (store
// \Deleted + expunge) each message.
func TestPollIMAPDeleteMode(t *testing.T) {
	allowLoopback(t)
	src := fakeIMAP(t, "From: a\r\n\r\nbody")
	store := &fakeWorkerStore{configs: []directory.FetchmailEntry{imapEntry(src, false)}}
	deliver, got := capture()

	n, errs := Poll(context.Background(), store, deliver, pollTime)
	if len(errs) != 0 {
		t.Fatalf("poll errors: %v", errs)
	}
	if n != 2 || len(*got) != 2 {
		t.Errorf("delivered %d, want 2 (both searched UIDs)", n)
	}
	joined := strings.Join(src.recorded(), " | ")
	if !strings.Contains(joined, `\Deleted`) || !strings.Contains(joined, "EXPUNGE") {
		t.Errorf("non-kept IMAP did not delete+expunge: %q", joined)
	}
	if strings.Contains(joined, `\Seen`) {
		t.Errorf("non-kept IMAP marked \\Seen instead of deleting: %q", joined)
	}
}

// TestPollIMAPKeepMarksSeen proves a kept IMAP account delivers and marks each \Seen (the
// dedup for the next poll) without deleting.
func TestPollIMAPKeepMarksSeen(t *testing.T) {
	allowLoopback(t)
	src := fakeIMAP(t, "From: a\r\n\r\nbody")
	store := &fakeWorkerStore{configs: []directory.FetchmailEntry{imapEntry(src, true)}}
	deliver, got := capture()

	n, errs := Poll(context.Background(), store, deliver, pollTime)
	if len(errs) != 0 {
		t.Fatalf("poll errors: %v", errs)
	}
	if n != 2 || len(*got) != 2 {
		t.Errorf("delivered %d, want 2", n)
	}
	joined := strings.Join(src.recorded(), " | ")
	if !strings.Contains(joined, `\Seen`) {
		t.Errorf("kept IMAP did not mark \\Seen: %q", joined)
	}
	if strings.Contains(joined, `\Deleted`) || strings.Contains(joined, "EXPUNGE") {
		t.Errorf("kept IMAP deleted from the source: %q", joined)
	}
}
