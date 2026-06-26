package mta

import (
	"bytes"
	"net/mail"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
)

// namingAccounts is a StaticAccounts that also resolves per-tenant outgoing display
// names, so DeliverAndRelay's optional senderNameResolver path is exercised.
type namingAccounts struct {
	directory.StaticAccounts
	internal, external string
}

func (a namingAccounts) OutgoingDisplayNames(string) (string, string, error) {
	return a.internal, a.external, nil
}

// TestDeliverAndRelayPerTenantName proves the From display name is rewritten per
// delivery leg: the locally filed copy carries the internal template's name and it
// survives the store's MIME->MAPI->read round-trip, while the relayed copy carries
// the external template's name.
func TestDeliverAndRelayPerTenantName(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := namingAccounts{
		StaticAccounts: directory.StaticAccounts{"alice@local": {MailboxPath: mbox}},
		internal:       "Ali Veli (Sales)",
		external:       "Ali Veli (Acme - Sales)",
	}
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	raw := []byte("From: alice@local\r\nTo: bob@remote\r\nSubject: hi\r\n\r\nhello\r\n")
	if _, err := DeliverAndRelay(accounts, sp, "alice@local",
		[]string{"alice@local", "bob@remote"}, raw, time.Now()); err != nil {
		t.Fatal(err)
	}

	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, _ := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if len(msgs) != 1 {
		t.Fatalf("local inbox = %d messages, want 1", len(msgs))
	}
	localRaw, err := st.GetMessageRaw(int64(mapi.PrivateFIDInbox), msgs[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	if got := fromName(t, localRaw); got != "Ali Veli (Sales)" {
		t.Errorf("local From name = %q, want the internal %q", got, "Ali Veli (Sales)")
	}

	due, _ := sp.Claim(time.Now(), 10)
	if len(due) != 1 {
		t.Fatalf("spool = %d items, want 1", len(due))
	}
	if got := fromName(t, due[0].Body); got != "Ali Veli (Acme - Sales)" {
		t.Errorf("relayed From name = %q, want the external %q", got, "Ali Veli (Acme - Sales)")
	}
}

func fromName(t *testing.T, raw []byte) string {
	t.Helper()
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse delivered message: %v", err)
	}
	a, err := mail.ParseAddress(m.Header.Get("From"))
	if err != nil {
		return ""
	}
	return a.Name
}
