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
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	// Authenticated submission.
	s := &session{accounts: accounts, spool: sp, authUser: "alice@local"}
	if err := s.Mail("alice@local"); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if err := s.Rcpt("alice@local"); err != nil {
		t.Fatalf("rcpt local: %v", err)
	}
	if err := s.Rcpt("bob@remote"); err != nil {
		t.Fatalf("rcpt external: %v", err)
	}
	if err := s.Rcpt("ghost@local"); err == nil {
		t.Error("an unknown user in a local domain must be refused, never relayed")
	}
	if len(s.targets) != 1 || s.targets[0].addr != "alice@local" {
		t.Fatalf("local targets = %v, want [alice@local]", s.targets)
	}
	if len(s.relayTargets) != 1 || s.relayTargets[0] != "bob@remote" {
		t.Fatalf("relay targets = %v, want [bob@remote]", s.relayTargets)
	}

	raw := []byte("Subject: hi\r\n\r\nhello\r\n")
	if err := s.Data(bytes.NewReader(raw)); err != nil {
		t.Fatalf("data: %v", err)
	}

	// The local recipient landed in the inbox.
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("local inbox has %d messages, want 1", len(msgs))
	}

	// The external recipient is durably queued for relay with the wire bytes intact.
	due, err := sp.Claim(time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Recipient != "bob@remote" {
		t.Fatalf("spool claim = %v, want one item for bob@remote", due)
	}
	if !bytes.Equal(due[0].Body, raw) {
		t.Error("spooled relay body does not match the submitted message")
	}

	// An unauthenticated session may not relay to an external recipient.
	u := &session{accounts: accounts, spool: sp}
	if err := u.Rcpt("bob@remote"); err == nil {
		t.Error("unauthenticated relay to an external recipient must be refused")
	}
}
