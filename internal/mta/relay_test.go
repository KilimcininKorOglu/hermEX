package mta

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
	"hermex/internal/smtp"
)

// TestSubmissionRelayRouting proves the local/external split of an authenticated
// submission: a recipient that resolves locally is filed into its mailbox, a
// recipient in a foreign domain is handed to the relay spool, and an unresolved
// recipient in a *local* domain is refused as user-unknown rather than relayed
// (which would loop). An unauthenticated session may not relay externally at all.
func TestSubmissionRelayRouting(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@local": {Password: "pw", MailboxPath: mbox}}
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	mustNoErr(t, "open the relay spool", err)
	defer sp.Close()

	// Authenticated submission.
	s := &session{accounts: accounts, spool: sp, authUser: "alice@local"}
	mustNoErr(t, "MAIL FROM", s.Mail("alice@local", smtp.MailParams{}))
	mustNoErr(t, "RCPT to a local user", s.Rcpt("alice@local", smtp.RcptParams{}))
	mustNoErr(t, "RCPT to an external user", s.Rcpt("bob@remote", smtp.RcptParams{}))
	if err := s.Rcpt("ghost@local", smtp.RcptParams{}); err == nil {
		t.Error("an unknown user in a local domain must be refused, never relayed")
	}
	if len(s.targets) != 1 || len(s.relayTargets) != 1 {
		t.Fatalf("routing = %d local / %d relay, want one each", len(s.targets), len(s.relayTargets))
	}
	wantEq(t, "the local target", s.targets[0].addr, "alice@local")
	wantEq(t, "the relay target", s.relayTargets[0].Addr, "bob@remote")

	raw := []byte("Subject: hi\r\n\r\nhello\r\n")
	mustNoErr(t, "DATA", s.Data(bytes.NewReader(raw)))

	// The local recipient landed in the inbox.
	wantEq(t, "messages in the local inbox", len(inboxMessages(t, mbox)), 1)

	// The external recipient is durably queued for relay with the wire bytes intact.
	due, err := sp.Claim(time.Now(), 10)
	mustNoErr(t, "claim the spool", err)
	if len(due) != 1 {
		t.Fatalf("spool claim = %v, want one item", due)
	}
	wantEq(t, "the spooled recipient", due[0].Recipient, "bob@remote")
	if !bytes.Equal(due[0].Body, raw) {
		t.Error("spooled relay body does not match the submitted message")
	}

	// An unauthenticated session may not relay to an external recipient.
	u := &session{accounts: accounts, spool: sp}
	if err := u.Rcpt("bob@remote", smtp.RcptParams{}); err == nil {
		t.Error("unauthenticated relay to an external recipient must be refused")
	}
}

// inboxMessages lists a mailbox's Inbox.
func inboxMessages(t *testing.T, mbox string) []objectstore.MessageInfo {
	t.Helper()
	st, err := objectstore.Open(mbox)
	mustNoErr(t, "open the mailbox", err)
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list the inbox", err)
	return msgs
}

// TestDeliverAndRelayRoutesExternal proves the shared user-composed send path,
// used by webmail compose and the send-later release, relays a foreign-domain
// recipient through the spool while still filing local ones and reporting a
// genuine local-domain user-unknown. With a nil spool it does not relay.
func TestDeliverAndRelayRoutesExternal(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@local": {MailboxPath: mbox}}
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	mustNoErr(t, "open the relay spool", err)
	defer sp.Close()

	raw := []byte("Subject: hi\r\n\r\nhello\r\n")
	unresolved, err := DeliverAndRelay(accounts, sp, "alice@local",
		[]string{"alice@local", "bob@remote", "ghost@local"}, raw, time.Now())
	mustNoErr(t, "deliver and relay", err)
	// Only the local-domain user-unknown is reported; the external is relayed.
	wantOnly(t, "the unresolved recipients", unresolved, "ghost@local")

	wantEq(t, "messages in the local inbox", len(inboxMessages(t, mbox)), 1)
	due, err := sp.Claim(time.Now(), 10)
	mustNoErr(t, "claim the spool", err)
	if len(due) != 1 {
		t.Fatalf("spool = %v, want one item queued for relay", due)
	}
	wantEq(t, "the spooled recipient", due[0].Recipient, "bob@remote")

	// With a nil spool the external recipient falls back to unresolved.
	un2, err := DeliverAndRelay(accounts, nil, "alice@local", []string{"bob@remote"}, raw, time.Now())
	mustNoErr(t, "deliver with no spool", err)
	wantOnly(t, "the unresolved recipients with no spool", un2, "bob@remote")
}

// wantOnly checks a recipient list holds exactly the one address wanted.
func wantOnly(t *testing.T, what string, got []string, want string) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("%s = %v, want only %q", what, got, want)
	}
	wantEq(t, what, got[0], want)
}
