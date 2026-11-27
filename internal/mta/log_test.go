package mta

import (
	"fmt"
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

// TestFailedSubmissionLoginIsRecordedWithoutThePassword proves a rejected SMTP
// AUTH records auth.fail with the user it tried and the client address, as IMAP
// and POP3 do, and that no event carries the password the client sent.
func TestFailedSubmissionLoginIsRecordedWithoutThePassword(t *testing.T) {
	const tried = "Wrong-Secret-Value-42"
	sink := &captureSink{}
	accounts := directory.StaticAccounts{"alice@test": {Password: "right", MailboxPath: t.TempDir()}}
	b := &Backend{Accounts: accounts, Logger: logging.New(sink)}
	sess, err := b.NewSession("203.0.113.5:1234")
	mustNoErr(t, "open the session", err)
	authn, ok := sess.(interface{ Auth(user, pass string) bool })
	if !ok {
		t.Fatal("the session offers no AUTH")
	}
	if authn.Auth("alice@test", tried) {
		t.Fatal("a wrong password was accepted")
	}
	if sess.(*session).authUser != "" {
		t.Fatalf("failed AUTH set authUser = %q", sess.(*session).authUser)
	}
	if authn.Auth("ghost@test", tried) {
		t.Fatal("unknown user accepted")
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if !hasAuthFail(sink.events, "alice@test", "203.0.113.5:1234") {
		t.Error("missing auth.fail for alice@test")
	}
	if !hasAuthFail(sink.events, "ghost@test", "203.0.113.5:1234") {
		t.Error("missing auth.fail for ghost@test")
	}
	for _, e := range sink.events {
		if strings.Contains(fmt.Sprintf("%+v", e), tried) {
			t.Errorf("event %q carries the password the client sent", e.Name)
		}
	}
}

func hasAuthFail(events []logging.Event, user, addr string) bool {
	for _, e := range events {
		if e.Name == "auth.fail" && e.User == user && e.RemoteAddr == addr {
			return true
		}
	}
	return false
}

// TestDeliveryLogsRecipient proves the MTA logs a delivery.ok event tagged with
// the recipient (User) and the envelope sender, and carrying the client address.
func TestDeliveryLogsRecipient(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(mbox)
	mustNoErr(t, "open the mailbox", err)
	mustClose(t, st)

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
