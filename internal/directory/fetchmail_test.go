package directory

import (
	"path/filepath"
	"testing"
)

// TestFetchmailCRUD covers the poll-config store: an entry round-trips its fields, the
// active-only listing excludes a disabled entry, an unknown protocol is rejected, delete
// reports existence, and deleting the owning user removes its entries.
func TestFetchmailCRUD(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "hermex.test")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("alice@hermex.test", "pw", filepath.Join(root, "alice")); err != nil {
		t.Fatal(err)
	}

	if list, err := d.ListFetchmail("alice@hermex.test"); err != nil || len(list) != 0 {
		t.Fatalf("fresh list = %v, %v; want empty", list, err)
	}

	id, err := d.CreateFetchmail(FetchmailEntry{
		Mailbox: "alice@hermex.test", Active: true,
		SrcServer: "mail.example.com", SrcPort: 993, SrcUser: "alice", SrcPassword: "secret",
		Protocol: "IMAP", SrcFolder: "INBOX", FetchAll: false, Keep: true, UseSSL: true, SSLVerify: true,
	})
	if err != nil {
		t.Fatalf("CreateFetchmail: %v", err)
	}

	list, err := d.ListFetchmail("alice@hermex.test")
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v; want one entry", list, err)
	}
	got := list[0]
	if got.SrcServer != "mail.example.com" || got.SrcPort != 993 || got.SrcUser != "alice" ||
		got.SrcPassword != "secret" || got.Protocol != "IMAP" || !got.Keep || !got.UseSSL || got.FetchAll {
		t.Errorf("entry did not round-trip: %+v", got)
	}

	// A disabled entry is excluded from the worker's active listing.
	if _, err := d.CreateFetchmail(FetchmailEntry{
		Mailbox: "alice@hermex.test", Active: false,
		SrcServer: "old.example.com", SrcUser: "alice", Protocol: "POP3",
	}); err != nil {
		t.Fatal(err)
	}
	active, err := d.ListActiveFetchmail()
	if err != nil || len(active) != 1 || active[0].ID != id {
		t.Errorf("active listing = %v, %v; want only the active entry", active, err)
	}

	// Validation rejects an unknown protocol before storage.
	if _, err := d.CreateFetchmail(FetchmailEntry{
		Mailbox: "alice@hermex.test", SrcServer: "x", SrcUser: "x", Protocol: "FTP",
	}); err == nil {
		t.Error("an unknown protocol was accepted; want rejected")
	}

	// Delete reports existence.
	if ok, err := d.DeleteFetchmail(id); err != nil || !ok {
		t.Errorf("DeleteFetchmail = %v, %v; want true", ok, err)
	}
	if ok, _ := d.DeleteFetchmail(id); ok {
		t.Error("second delete reported a row; want false")
	}

	// Deleting the user removes its remaining entries.
	if _, err := d.DeleteUser("alice@hermex.test", false); err != nil {
		t.Fatal(err)
	}
	if list, _ := d.ListFetchmail("alice@hermex.test"); len(list) != 0 {
		t.Errorf("after user delete, fetchmail entries = %v; want none", list)
	}
}

// TestFetchmailSeen covers the POP3 dedup state: recorded ids read back, a re-record is
// idempotent, and deleting the owning entry cascades its seen rows away.
func TestFetchmailSeen(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "hermex.test")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("alice@hermex.test", "pw", filepath.Join(root, "alice")); err != nil {
		t.Fatal(err)
	}
	id, err := d.CreateFetchmail(FetchmailEntry{
		Mailbox: "alice@hermex.test", Active: true, SrcServer: "s", SrcUser: "u", Protocol: "POP3", Keep: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := d.MarkFetchmailSeen(id, []string{"uidA", "uidB"}); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkFetchmailSeen(id, []string{"uidA"}); err != nil { // idempotent re-record
		t.Fatalf("re-record: %v", err)
	}
	seen, err := d.FetchmailSeen(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || !seen["uidA"] || !seen["uidB"] {
		t.Errorf("seen = %v, want {uidA, uidB}", seen)
	}

	// Deleting the entry cascades its seen rows.
	if _, err := d.DeleteFetchmail(id); err != nil {
		t.Fatal(err)
	}
	if seen, _ := d.FetchmailSeen(id); len(seen) != 0 {
		t.Errorf("after entry delete, seen = %v; want none (cascade)", seen)
	}
}
