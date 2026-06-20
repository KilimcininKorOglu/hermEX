package fetchmail

import (
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

// TestPollPOP3DeleteMode proves a non-kept POP3 account delivers every message and deletes
// each from the source (so the source itself prevents a re-fetch).
func TestPollPOP3DeleteMode(t *testing.T) {
	msgs := []string{"From: a\r\n\r\none", "From: b\r\n\r\ntwo"}
	host, port, deleted := fakePOP3(t, msgs)
	store := &fakeWorkerStore{configs: []directory.FetchmailEntry{{
		ID: 1, Mailbox: "alice@hermex.test", Active: true,
		SrcServer: host, SrcPort: port, SrcUser: "alice", SrcPassword: "pw",
		Protocol: "POP3", Keep: false,
	}}}
	deliver, got := capture()

	n, errs := Poll(store, deliver, pollTime)
	if len(errs) != 0 {
		t.Fatalf("poll errors: %v", errs)
	}
	if n != 2 || len(*got) != 2 || (*got)[0] != msgs[0]+"\r\n" {
		t.Errorf("delivered %d %q, want both messages", n, *got)
	}
	if len(*deleted) != 2 {
		t.Errorf("server deletions = %v, want both messages deleted", *deleted)
	}
}

// TestPollPOP3KeepDedup proves a kept POP3 account skips an already-seen id, delivers only
// the new message, records its id, and deletes nothing on the source.
func TestPollPOP3KeepDedup(t *testing.T) {
	msgs := []string{"From: a\r\n\r\none", "From: b\r\n\r\ntwo"}
	host, port, deleted := fakePOP3(t, msgs)
	store := &fakeWorkerStore{
		configs: []directory.FetchmailEntry{{
			ID: 1, Mailbox: "alice@hermex.test", Active: true,
			SrcServer: host, SrcPort: port, SrcUser: "alice", SrcPassword: "pw",
			Protocol: "POP3", Keep: true,
		}},
		seen: map[int64]map[string]bool{1: {"uid1": true}},
	}
	deliver, got := capture()

	n, errs := Poll(store, deliver, pollTime)
	if len(errs) != 0 {
		t.Fatalf("poll errors: %v", errs)
	}
	if n != 1 || len(*got) != 1 || (*got)[0] != msgs[1]+"\r\n" {
		t.Errorf("delivered %d %q, want only the unseen message", n, *got)
	}
	if want := []string{"uid2"}; len(store.marked[1]) != 1 || store.marked[1][0] != want[0] {
		t.Errorf("recorded seen = %v, want %v", store.marked[1], want)
	}
	if len(*deleted) != 0 {
		t.Errorf("a kept account deleted from the source: %v", *deleted)
	}
}

// TestPollIMAPDeleteMode proves a non-kept IMAP account delivers and deletes (store
// \Deleted + expunge) each message.
func TestPollIMAPDeleteMode(t *testing.T) {
	host, port, recorded := fakeIMAP(t, "From: a\r\n\r\nbody")
	store := &fakeWorkerStore{configs: []directory.FetchmailEntry{{
		ID: 1, Mailbox: "alice@hermex.test", Active: true,
		SrcServer: host, SrcPort: port, SrcUser: "alice", SrcPassword: "pw",
		Protocol: "IMAP", SrcFolder: "INBOX", Keep: false,
	}}}
	deliver, got := capture()

	n, errs := Poll(store, deliver, pollTime)
	if len(errs) != 0 {
		t.Fatalf("poll errors: %v", errs)
	}
	if n != 2 || len(*got) != 2 {
		t.Errorf("delivered %d, want 2 (both searched UIDs)", n)
	}
	joined := strings.Join(*recorded, " | ")
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
	host, port, recorded := fakeIMAP(t, "From: a\r\n\r\nbody")
	store := &fakeWorkerStore{configs: []directory.FetchmailEntry{{
		ID: 1, Mailbox: "alice@hermex.test", Active: true,
		SrcServer: host, SrcPort: port, SrcUser: "alice", SrcPassword: "pw",
		Protocol: "IMAP", SrcFolder: "INBOX", Keep: true,
	}}}
	deliver, got := capture()

	n, errs := Poll(store, deliver, pollTime)
	if len(errs) != 0 {
		t.Fatalf("poll errors: %v", errs)
	}
	if n != 2 || len(*got) != 2 {
		t.Errorf("delivered %d, want 2", n)
	}
	joined := strings.Join(*recorded, " | ")
	if !strings.Contains(joined, `\Seen`) {
		t.Errorf("kept IMAP did not mark \\Seen: %q", joined)
	}
	if strings.Contains(joined, `\Deleted`) || strings.Contains(joined, "EXPUNGE") {
		t.Errorf("kept IMAP deleted from the source: %q", joined)
	}
}
