package mta

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
	"hermex/internal/smtp"
)

// deliveryEvents runs one inbound transaction to a recipient whose mailbox is
// expected to refuse the delivery, and returns every event it emitted. The
// transaction's own errors are the point of the test, so they are not asserted
// here.
func deliveryEvents(t *testing.T, mbox string) []logging.Event {
	t.Helper()
	sink := &captureSink{}
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	b := &Backend{Accounts: accounts, Logger: logging.New(sink)}
	sess, err := b.NewSession("203.0.113.5:1234")
	mustNoErr(t, "open the session", err)
	mustNoErr(t, "MAIL FROM", sess.Mail("sender@example.com", smtp.MailParams{}))
	mustNoErr(t, "RCPT TO", sess.Rcpt("alice@test", smtp.RcptParams{}))
	if err := sess.Data(strings.NewReader("Subject: hi\r\n\r\nbody\r\n")); err == nil {
		t.Fatal("DATA succeeded, so the mailbox did not refuse the delivery")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]logging.Event(nil), sink.events...)
}

// findEvent returns the first event carrying name.
func findEvent(events []logging.Event, name string) (logging.Event, bool) {
	for _, e := range events {
		if e.Name == name {
			return e, true
		}
	}
	return logging.Event{}, false
}

// condemnedMailbox provisions a mailbox and then makes its object database
// permanently unreadable, which is what a corrupted store looks like to
// delivery: SQLite reports it as not a database, and no retry will change that.
func condemnedMailbox(t *testing.T) string {
	t.Helper()
	mbox := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(mbox)
	mustNoErr(t, "open the mailbox", err)
	st.Close()
	mustNoErr(t, "overwrite the object database",
		os.WriteFile(filepath.Join(mbox, "objects.sqlite3"), []byte("not a database"), 0o600))
	return mbox
}

// TestAnUnusableMailboxIsRecordedUnderItsOwnEvent is the defect this change
// fixes. A condemned mailbox database is deferred exactly like a disk that is
// briefly unavailable, so the sender retries for days and the operator sees
// nothing but repeated delivery.fail lines. The distinct event is what makes the
// mailbox visible as needing repair.
func TestAnUnusableMailboxIsRecordedUnderItsOwnEvent(t *testing.T) {
	mbox := condemnedMailbox(t)
	events := deliveryEvents(t, mbox)

	if _, ok := findEvent(events, "delivery.fail"); !ok {
		t.Error("no delivery.fail event for the refused delivery")
	}
	e, ok := findEvent(events, "delivery.mailbox_unusable")
	if !ok {
		t.Fatalf("no delivery.mailbox_unusable event; got %v", eventNames(events))
	}
	wantEq(t, "the event level", e.Level, logging.LevelError)
	wantEq(t, "the recorded recipient", e.User, "alice@test")
	wantEq(t, "the recorded mailbox", e.Fields["mailbox"], any(mbox))
	if e.Err == "" {
		t.Error("the event carries no error text")
	}
}

// TestATransientDeliveryFailureIsNotRecordedAsUnusable holds the other side. A
// mailbox that cannot be opened for a reason that may clear on its own must not
// be reported as condemned, or the page an operator repairs from fills with
// mailboxes that need no repair. Here the mailbox path is an ordinary file, so
// the store fails before SQLite is reached at all.
func TestATransientDeliveryFailureIsNotRecordedAsUnusable(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	mustNoErr(t, "write a file where the mailbox belongs", os.WriteFile(mbox, []byte("x"), 0o600))

	events := deliveryEvents(t, mbox)
	if _, ok := findEvent(events, "delivery.fail"); !ok {
		t.Error("no delivery.fail event for the refused delivery")
	}
	if _, ok := findEvent(events, "delivery.mailbox_unusable"); ok {
		t.Error("a failure that is not a condemned database was recorded as an unusable mailbox")
	}
}

// eventNames lists the emitted event names for a failure message.
func eventNames(events []logging.Event) []string {
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, e.Name)
	}
	return names
}
