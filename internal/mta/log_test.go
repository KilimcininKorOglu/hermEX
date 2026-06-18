package mta

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
)

// captureSink records every event for assertion.
type captureSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (c *captureSink) Write(e logging.Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

// TestDeliveryLogsRecipient proves the MTA logs a delivery.ok event tagged with
// the recipient (User) and the envelope sender, and carrying the client address.
func TestDeliveryLogsRecipient(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	sink := &captureSink{}
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	b := &Backend{Accounts: accounts, Logger: logging.New(sink)}
	sess, err := b.NewSession("203.0.113.5:1234")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Mail("sender@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Rcpt("alice@test"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Data(strings.NewReader("Subject: hi\r\n\r\nbody\r\n")); err != nil {
		t.Fatalf("Data: %v", err)
	}

	sink.mu.Lock()
	events := append([]logging.Event(nil), sink.events...)
	sink.mu.Unlock()

	var found bool
	for _, e := range events {
		if e.Name != "delivery.ok" {
			continue
		}
		found = true
		if e.User != "alice@test" {
			t.Errorf("delivery.ok user = %q, want alice@test", e.User)
		}
		if e.Fields["from"] != "sender@example.com" {
			t.Errorf("delivery.ok from = %v, want sender@example.com", e.Fields["from"])
		}
		if e.RemoteAddr != "203.0.113.5:1234" {
			t.Errorf("delivery.ok remote = %q, want the client address", e.RemoteAddr)
		}
	}
	if !found {
		t.Error("no delivery.ok event for the delivered recipient")
	}
}
