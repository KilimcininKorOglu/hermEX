package mta

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/relay"
	"hermex/internal/smtp"
)

// sizedSend builds a local account and a spool with the message size limit set to
// n bytes for one test.
func sizedSend(t *testing.T, n int64) (directory.StaticAccounts, *relay.Spool) {
	t.Helper()
	spool, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustClose(t, spool) })

	SetMaxMessageSize(n)
	t.Cleanup(func() { SetMaxMessageSize(directory.DefaultMaxInboundBytes) })

	return directory.StaticAccounts{"alice@acme.test": {Password: "pw", MailboxPath: t.TempDir()}}, spool
}

// TestDeliverAndRelayRefusesOversizedMessage proves the operator's limit now
// covers the paths that never reach an SMTP session: webmail, EWS, ActiveSync,
// ROP, DAV scheduling and the send-later release all arrive here.
func TestDeliverAndRelayRefusesOversizedMessage(t *testing.T) {
	accounts, spool := sizedSend(t, 1024)
	raw := make([]byte, 1025)
	copy(raw, "Subject: big\r\n\r\n")

	unresolved, err := DeliverAndRelay(accounts, spool, "alice@acme.test", []string{"alice@acme.test", "a@far.test"}, raw, time.Unix(1000, 0))
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("err = %v, want ErrMessageTooLarge", err)
	}
	wantEq(t, "unresolved on size refusal", len(unresolved), 2)
	wantEq(t, "local inbox after size refusal", inboxCount(t, accounts["alice@acme.test"].MailboxPath), 0)
	entries, err := spool.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%d entries were queued despite the refusal", len(entries))
	}

	exact := make([]byte, 1024)
	copy(exact, "Subject: ok\r\n\r\n")
	unresolved, err = DeliverAndRelay(accounts, spool, "alice@acme.test", []string{"a@far.test"}, exact, time.Unix(1000, 0))
	if err != nil || len(unresolved) != 0 {
		t.Fatalf("exact limit send: unresolved=%v err=%v", unresolved, err)
	}
}

// TestSubmissionBoundsRecipientsPerMessage sends one message to one recipient
// more than SMTP accepts in a transaction. Every client that is not SMTP
// (webmail, EWS, ActiveSync, ROP, DAV scheduling, send-later) converges here, so
// the RFC 5321 4.5.3.1.10 bound must hold here too: the send is refused whole
// and terminally, nothing is filed or queued, and a message at the bound sends.
func TestSubmissionBoundsRecipientsPerMessage(t *testing.T) {
	accounts, spool := sizedSend(t, 0)
	raw := []byte("Subject: many\r\n\r\nbody\r\n")
	recipients := make([]string, smtp.MaxRecipients+1)
	for i := range recipients {
		recipients[i] = "r" + strconv.Itoa(i) + "@far.test"
	}

	unresolved, err := DeliverAndRelay(accounts, spool, "alice@acme.test", recipients, raw, time.Unix(1000, 0))
	if !errors.Is(err, ErrTooManyRecipients) {
		t.Fatalf("err = %v, want ErrTooManyRecipients", err)
	}
	wantEq(t, "unresolved on recipient bound refusal", len(unresolved), len(recipients))
	wantTerminal(t, err)
	wantSpoolEmpty(t, spool)

	keep, err := SendRenderings(accounts, spool, "alice@acme.test",
		recipients[:smtp.MaxRecipients/2+1], raw, recipients[smtp.MaxRecipients/2+1:], raw, time.Unix(1000, 0))
	if !errors.Is(err, ErrTooManyRecipients) || !keep {
		t.Fatalf("two renderings over the bound: err = %v keep = %v, want ErrTooManyRecipients", err, keep)
	}
	wantSpoolEmpty(t, spool)

	unresolved, err = DeliverAndRelay(accounts, spool, "alice@acme.test", recipients[:smtp.MaxRecipients], raw, time.Unix(1000, 0))
	mustNoErr(t, "a message at the bound", err)
	wantEq(t, "unresolved at bound", len(unresolved), 0)
}

func wantTerminal(t *testing.T, err error) {
	t.Helper()
	var terminal interface{ TerminalDelivery() bool }
	if !errors.As(err, &terminal) || !terminal.TerminalDelivery() {
		t.Error("the refusal must be terminal: the recipient list will not shrink on a retry")
	}
}

func wantSpoolEmpty(t *testing.T, spool *relay.Spool) {
	t.Helper()
	entries, err := spool.List()
	mustNoErr(t, "list spool", err)
	wantEq(t, "queued spool entries", len(entries), 0)
}

// TestOversizedRefusalIsTerminal proves the refusal is not retried: the message
// will not shrink, so a scheduled send must be dropped rather than re-attempted
// until it expires.
func TestOversizedRefusalIsTerminal(t *testing.T) {
	var terminal interface{ TerminalDelivery() bool }
	if !errors.As(ErrMessageTooLarge, &terminal) || !terminal.TerminalDelivery() {
		t.Error("ErrMessageTooLarge must report itself terminal")
	}
}

// TestDeliverAndRelayAdmitsMessageUnderTheLimit keeps ordinary sending working.
func TestDeliverAndRelayAdmitsMessageUnderTheLimit(t *testing.T) {
	accounts, spool := sizedSend(t, 4096)
	raw := []byte("Subject: fine\r\n\r\n" + strings.Repeat("x", 100))

	unresolved, err := DeliverAndRelay(accounts, spool, "alice@acme.test", []string{"a@far.test"}, raw, time.Unix(1000, 0))
	if err != nil || len(unresolved) != 0 {
		t.Fatalf("send under the limit failed: unresolved=%v err=%v", unresolved, err)
	}
}

// TestNoLimitAdmitsEverything covers the default: with no stored setting the size
// check must not reject anything.
func TestNoLimitAdmitsEverything(t *testing.T) {
	accounts, spool := sizedSend(t, 0)
	raw := []byte("Subject: huge\r\n\r\n" + strings.Repeat("x", 1<<20))

	unresolved, err := DeliverAndRelay(accounts, spool, "alice@acme.test", []string{"a@far.test"}, raw, time.Unix(1000, 0))
	if err != nil || len(unresolved) != 0 {
		t.Fatalf("send with no limit configured failed: unresolved=%v err=%v", unresolved, err)
	}
}

// TestApplyMessageSizeSettings covers the settings plumbing every daemon shares:
// no stored row leaves the current limit alone, a stored row applies at once.
func TestApplyMessageSizeSettings(t *testing.T) {
	SetMaxMessageSize(0)
	t.Cleanup(func() { SetMaxMessageSize(directory.DefaultMaxInboundBytes) })

	ApplyMessageSizeSettings("test", nil, func() (MessageSizeSettings, bool, error) {
		return MessageSizeSettings{MaxInboundBytes: 4096}, false, nil
	})
	if overMessageSize(make([]byte, 8192)) {
		t.Error("an unsaved settings row applied a limit")
	}

	ApplyMessageSizeSettings("test", nil, func() (MessageSizeSettings, bool, error) {
		return MessageSizeSettings{MaxInboundBytes: 4096}, true, nil
	})
	if !overMessageSize(make([]byte, 8192)) {
		t.Error("the stored limit was not applied")
	}
	if overMessageSize(make([]byte, 4096)) {
		t.Error("a message exactly at the limit was refused")
	}
}
