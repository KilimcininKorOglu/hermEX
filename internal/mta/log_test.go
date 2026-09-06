package mta

import (
	"path/filepath"
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
	mustNoErr(t, "open the mailbox", err)
	st.Close()

	sink := &captureSink{}
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	b := &Backend{Accounts: accounts, Logger: logging.New(sink)}
	deliverBody(t, b, "203.0.113.5:1234", "sender@example.com", "alice@test", "Subject: hi\r\n\r\nbody\r\n")

	sink.mu.Lock()
	events := append([]logging.Event(nil), sink.events...)
	sink.mu.Unlock()

	var found bool
	for _, e := range events {
		if e.Name != "delivery.ok" {
			continue
		}
		found = true
		wantEq(t, "the delivery.ok user", e.User, "alice@test")
		wantEq(t, "the delivery.ok sender", e.Fields["from"], "sender@example.com")
		wantEq(t, "the delivery.ok client address", e.RemoteAddr, "203.0.113.5:1234")
	}
	if !found {
		t.Error("no delivery.ok event for the delivered recipient")
	}
}
