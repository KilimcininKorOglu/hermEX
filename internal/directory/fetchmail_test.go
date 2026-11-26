package directory

import (
	"strings"
	"testing"
)

// TestFetchmailCRUD covers the poll-config store: an entry round-trips its fields, the
// active-only listing excludes a disabled entry, an unknown protocol is rejected, delete
// reports existence, and deleting the owning user removes its entries.
func TestFetchmailCRUD(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "alice@hermex.test", "password1")

	wantEq(t, "entries of a fresh mailbox", len(mustListFetchmail(t, d)), 0)

	id, err := d.CreateFetchmail(FetchmailEntry{
		Mailbox: "alice@hermex.test", Active: true,
		SrcServer: "mail.example.com", SrcPort: 993, SrcUser: "alice", SrcPassword: "secret",
		Protocol: "IMAP", SrcFolder: "INBOX", FetchAll: false, Keep: true, UseSSL: true, SSLVerify: true,
	})
	mustNoErr(t, "create fetchmail entry", err)

	list := mustListFetchmail(t, d)
	if len(list) != 1 {
		t.Fatalf("list = %v, want one entry", list)
	}
	wantFetchmailRoundTrip(t, list[0])

	// A disabled entry is excluded from the worker's active listing.
	_, err = d.CreateFetchmail(FetchmailEntry{
		Mailbox: "alice@hermex.test", Active: false,
		SrcServer: "old.example.com", SrcUser: "alice", Protocol: "POP3",
	})
	mustNoErr(t, "create a disabled entry", err)
	active, err := d.ListActiveFetchmail()
	mustNoErr(t, "list active entries", err)
	if len(active) != 1 {
		t.Fatalf("active listing = %v, want only the active entry", active)
	}
	wantEq(t, "the active entry", active[0].ID, id)

	// Validation rejects an unknown protocol before storage.
	_, err = d.CreateFetchmail(FetchmailEntry{
		Mailbox: "alice@hermex.test", SrcServer: "x", SrcUser: "x", Protocol: "FTP",
	})
	wantErr(t, "an unknown protocol was accepted", err)
	wantEq(t, "entries after the refused create", len(mustListFetchmail(t, d)), 2)

	// Another mailbox sees none of alice's entries.
	other, err := d.ListFetchmail("bob@hermex.test")
	mustNoErr(t, "list another mailbox's entries", err)
	wantEq(t, "entries listed for another mailbox", len(other), 0)

	// Delete reports existence.
	deleted, err := d.DeleteFetchmail(id)
	mustNoErr(t, "delete fetchmail entry", err)
	wantEq(t, "DeleteFetchmail reported the entry existed", deleted, true)
	again, err := d.DeleteFetchmail(id)
	mustNoErr(t, "delete the entry again", err)
	wantEq(t, "the second delete", again, false)

	// Deleting the user removes its remaining entries.
	_, err = d.DeleteUser("alice@hermex.test", false)
	mustNoErr(t, "delete user", err)
	wantEq(t, "fetchmail entries after the user delete", len(mustListFetchmail(t, d)), 0)
}

// TestFetchmailRefusesAMailboxThatDoesNotExist proves a poll configuration needs
// a live account to deliver into. Nothing ties fetchmail.mailbox to a user row,
// so an entry for an unknown address would sit idle until someone is later
// created under that name and then pull another server's mail into that account.
func TestFetchmailRefusesAMailboxThatDoesNotExist(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	for _, mbox := range []string{"ghost@hermex.test", "ghost@unhosted.test", ""} {
		_, err := d.CreateFetchmail(FetchmailEntry{Mailbox: mbox, SrcServer: "s", SrcUser: "u", Protocol: "IMAP"})
		wantErr(t, "an entry for mailbox "+mbox+" was accepted", err)
		list, err := d.ListFetchmail(mbox)
		mustNoErr(t, "list entries of "+mbox, err)
		wantEq(t, "entries stored for "+mbox, len(list), 0)
	}
}

// storedFetchmailPassword reads an entry's src_password column as stored.
func storedFetchmailPassword(t *testing.T, d *SQLDirectory, id int64) string {
	t.Helper()
	var s string
	mustNoErr(t, "read the stored source password",
		d.db.QueryRow(`SELECT src_password FROM fetchmail WHERE id = ?`, id).Scan(&s))
	return s
}

// TestFetchmailPasswordIsEncryptedAtRest stores a source password with a key
// secret installed. The password opens another server's mailbox, so a dump of
// the directory must not contain it, while the worker still reads it back.
func TestFetchmailPasswordIsEncryptedAtRest(t *testing.T) {
	d, _ := freshDirectory(t)
	d.SetKeySecret([]byte("test-key-secret"))
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "alice@hermex.test", "password1")
	long := strings.Repeat("p", maxFetchmailPassword)
	id, err := d.CreateFetchmail(FetchmailEntry{Mailbox: "alice@hermex.test", Active: true,
		SrcServer: "s", SrcUser: "u", SrcPassword: long, Protocol: "IMAP"})
	mustNoErr(t, "create an entry with the longest allowed password", err)
	if s := storedFetchmailPassword(t, d, id); !strings.HasPrefix(s, wrapPrefix) || strings.Contains(s, "ppp") {
		t.Errorf("the source password is stored readable: %.40q", s)
	}
	active, err := d.ListActiveFetchmail()
	mustNoErr(t, "list active entries", err)
	if len(active) != 1 || active[0].SrcPassword != long {
		t.Errorf("the worker did not get the password back")
	}
	_, err = d.CreateFetchmail(FetchmailEntry{Mailbox: "alice@hermex.test", SrcServer: "s", SrcUser: "u",
		SrcPassword: long + "p", Protocol: "IMAP"})
	wantErr(t, "a password over the limit was accepted", err)
	wantEq(t, "entries after the refused password", len(mustListFetchmail(t, d)), 1)
}

// TestFetchmailPlaintextPasswordIsRewrapped proves an entry written before the
// secret existed converges on encryption at its first read.
func TestFetchmailPlaintextPasswordIsRewrapped(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "alice@hermex.test", "password1")
	id, err := d.CreateFetchmail(FetchmailEntry{Mailbox: "alice@hermex.test", Active: true,
		SrcServer: "s", SrcUser: "u", SrcPassword: "legacy-secret", Protocol: "POP3"})
	mustNoErr(t, "create a plaintext entry", err)
	wantEq(t, "the entry starts as plaintext", storedFetchmailPassword(t, d, id), "legacy-secret")

	d.SetKeySecret([]byte("test-key-secret"))
	list := mustListFetchmail(t, d)
	if len(list) != 1 || list[0].SrcPassword != "legacy-secret" {
		t.Fatalf("list = %+v, want the original password", list)
	}
	if s := storedFetchmailPassword(t, d, id); !strings.HasPrefix(s, wrapPrefix) {
		t.Errorf("the plaintext password was not rewritten: %q", s)
	}
}

// mustListFetchmail lists alice's poll configurations.
func mustListFetchmail(t *testing.T, d *SQLDirectory) []FetchmailEntry {
	t.Helper()
	list, err := d.ListFetchmail("alice@hermex.test")
	mustNoErr(t, "list fetchmail entries", err)
	return list
}

// wantFetchmailRoundTrip checks every stored field came back as written.
func wantFetchmailRoundTrip(t *testing.T, got FetchmailEntry) {
	t.Helper()
	wantEq(t, "source server", got.SrcServer, "mail.example.com")
	wantEq(t, "source port", got.SrcPort, 993)
	wantEq(t, "source user", got.SrcUser, "alice")
	wantEq(t, "source password", got.SrcPassword, "secret")
	wantEq(t, "protocol", got.Protocol, "IMAP")
	wantEq(t, "keep", got.Keep, true)
	wantEq(t, "use ssl", got.UseSSL, true)
	wantEq(t, "fetch all", got.FetchAll, false)
}

// TestFetchmailSeen covers the POP3 dedup state: recorded ids read back, a re-record is
// idempotent, and deleting the owning entry cascades its seen rows away.
func TestFetchmailSeen(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "alice@hermex.test", "password1")
	id, err := d.CreateFetchmail(FetchmailEntry{
		Mailbox: "alice@hermex.test", Active: true, SrcServer: "s", SrcUser: "u", Protocol: "POP3", Keep: true,
	})
	mustNoErr(t, "create fetchmail entry", err)

	mustNoErr(t, "record seen ids", d.MarkFetchmailSeen(id, []string{"uidA", "uidB"}))
	mustNoErr(t, "re-record a seen id", d.MarkFetchmailSeen(id, []string{"uidA"}))
	seen, err := d.FetchmailSeen(id)
	mustNoErr(t, "read seen ids", err)
	wantEq(t, "seen id count", len(seen), 2)
	wantEq(t, "uidA seen", seen["uidA"], true)
	wantEq(t, "uidB seen", seen["uidB"], true)

	// Deleting the entry cascades its seen rows.
	_, err = d.DeleteFetchmail(id)
	mustNoErr(t, "delete fetchmail entry", err)
	after, err := d.FetchmailSeen(id)
	mustNoErr(t, "read seen ids", err)
	wantEq(t, "seen rows after the entry delete (cascade)", len(after), 0)
}
