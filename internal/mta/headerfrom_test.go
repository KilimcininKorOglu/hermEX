package mta

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
	"hermex/internal/smtp"
)

// submission opens an authenticated session for alice with a relay spool, with
// bob in her domain and carol in a second hosted domain, routes one local and one
// external recipient, and returns the session, the spool and bob's mailbox.
func submission(t *testing.T) (*session, *relay.Spool, string) {
	t.Helper()
	dir := t.TempDir()
	bob := filepath.Join(dir, "bob")
	accounts := directory.StaticAccounts{
		"alice@local":  {Password: "pw", MailboxPath: filepath.Join(dir, "alice")},
		"bob@local":    {Password: "pw", MailboxPath: bob},
		"carol@second": {Password: "pw", MailboxPath: filepath.Join(dir, "carol")},
	}
	sp, err := relay.Open(filepath.Join(dir, "relay.sqlite3"))
	mustNoErr(t, "open the relay spool", err)
	t.Cleanup(func() { mustNoErr(t, "close the relay spool", sp.Close()) })
	s := &session{accounts: accounts, spool: sp, authUser: "alice@local"}
	mustNoErr(t, "MAIL FROM", s.Mail("alice@local", smtp.MailParams{}))
	mustNoErr(t, "RCPT to a local user", s.Rcpt("bob@local", smtp.RcptParams{}))
	mustNoErr(t, "RCPT to an external user", s.Rcpt("dave@remote", smtp.RcptParams{}))
	return s, sp, bob
}

// inboxMessagesIfAny is inboxMessages for a mailbox delivery may never have
// created.
func inboxMessagesIfAny(t *testing.T, mbox string) []objectstore.MessageInfo {
	t.Helper()
	info, err := os.Stat(mbox)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	mustNoErr(t, "stat mailbox", err)
	if !info.IsDir() {
		t.Fatalf("mailbox path %q is not a directory", mbox)
	}
	return inboxMessages(t, mbox)
}

// TestSubmissionRefusesAForgedHeaderFrom proves authenticated submission applies
// the send-as gate to the From header, not only to MAIL FROM. The relay signer
// picks the DKIM domain from the From header, so an own envelope under another
// user's From would leave signed as that user. Each forged shape is refused with
// nothing filed and nothing queued: another user of the caller's domain, a user of
// a second hosted domain, a From list, and a second From field (RFC 5322 3.6
// allows one).
func TestSubmissionRefusesAForgedHeaderFrom(t *testing.T) {
	for _, tc := range []struct{ name, from string }{
		{"another local user", "From: bob@local\r\n"},
		{"a user of another hosted domain", "From: carol@second\r\n"},
		{"a list naming another user", "From: alice@local, bob@local\r\n"},
		{"a second From field", "From: alice@local\r\nFrom: carol@second\r\n"},
		{"unparseable From field", "From: not an address @@@\r\n"},
	} {
		s, sp, bob := submission(t)
		raw := []byte(tc.from + "To: bob@local, dave@remote\r\nSubject: hi\r\n\r\nhello\r\n")
		err := s.Data(bytes.NewReader(raw))
		if _, ok := err.(*smtp.PermError); !ok {
			t.Errorf("%s: DATA = %v, want a permanent refusal", tc.name, err)
		}
		if n := len(inboxMessagesIfAny(t, bob)); n != 0 {
			t.Errorf("%s: %d messages filed for bob, want 0", tc.name, n)
		}
		due, err := sp.Claim(time.Now(), 10)
		mustNoErr(t, "claim the spool", err)
		if len(due) != 0 {
			t.Errorf("%s: %d messages queued for relay, want 0", tc.name, len(due))
		}
	}
}

// TestSubmissionAcceptsAnOwnOrAbsentHeaderFrom is the regression side: the
// caller's own From, and a message with no From at all, still go out.
func TestSubmissionAcceptsAnOwnOrAbsentHeaderFrom(t *testing.T) {
	for _, from := range []string{"From: Alice <alice@local>\r\n", ""} {
		s, sp, bob := submission(t)
		raw := []byte(from + "To: bob@local, dave@remote\r\nSubject: hi\r\n\r\nhello\r\n")
		mustNoErr(t, "DATA", s.Data(bytes.NewReader(raw)))
		wantEq(t, "messages filed for bob", len(inboxMessagesIfAny(t, bob)), 1)
		due, err := sp.Claim(time.Now(), 10)
		mustNoErr(t, "claim the spool", err)
		wantEq(t, "messages queued for relay", len(due), 1)
	}
}

// TestInboundKeepsAnyHeaderFrom proves the gate is submission-only: a remote
// server delivering to a local user carries whatever From its sender wrote.
func TestInboundKeepsAnyHeaderFrom(t *testing.T) {
	bob := filepath.Join(t.TempDir(), "bob")
	s := &session{accounts: directory.StaticAccounts{"bob@local": {MailboxPath: bob}}}
	mustNoErr(t, "MAIL FROM", s.Mail("someone@remote", smtp.MailParams{}))
	mustNoErr(t, "RCPT", s.Rcpt("bob@local", smtp.RcptParams{}))
	raw := []byte("From: carol@second\r\nTo: bob@local\r\nSubject: hi\r\n\r\nhello\r\n")
	mustNoErr(t, "DATA", s.Data(bytes.NewReader(raw)))
	wantEq(t, "messages filed for bob", len(inboxMessagesIfAny(t, bob)), 1)
}
