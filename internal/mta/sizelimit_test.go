package mta

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/relay"
)

// sizedSend builds a local account and a spool with the message size limit set to
// n bytes for one test.
func sizedSend(t *testing.T, n int64) (directory.Accounts, *relay.Spool) {
	t.Helper()
	spool, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = spool.Close() })

	SetMaxMessageSize(n)
	t.Cleanup(func() { SetMaxMessageSize(0) })

	return directory.StaticAccounts{"alice@acme.test": {Password: "pw", MailboxPath: t.TempDir()}}, spool
}

// TestDeliverAndRelayRefusesOversizedMessage proves the operator's limit now
// covers the paths that never reach an SMTP session: webmail, EWS, ActiveSync,
// ROP, DAV scheduling and the send-later release all arrive here.
func TestDeliverAndRelayRefusesOversizedMessage(t *testing.T) {
	accounts, spool := sizedSend(t, 1024)
	raw := []byte("Subject: big\r\n\r\n" + strings.Repeat("x", 2048))

	_, err := DeliverAndRelay(accounts, spool, "alice@acme.test", []string{"a@far.test"}, raw, time.Unix(1000, 0))
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("err = %v, want ErrMessageTooLarge", err)
	}
	entries, err := spool.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%d entries were queued despite the refusal", len(entries))
	}
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

	if _, err := DeliverAndRelay(accounts, spool, "alice@acme.test", []string{"a@far.test"}, raw, time.Unix(1000, 0)); err != nil {
		t.Fatalf("send under the limit failed: %v", err)
	}
}

// TestNoLimitAdmitsEverything covers the default: with no stored setting the size
// check must not reject anything.
func TestNoLimitAdmitsEverything(t *testing.T) {
	accounts, spool := sizedSend(t, 0)
	raw := []byte("Subject: huge\r\n\r\n" + strings.Repeat("x", 1<<20))

	if _, err := DeliverAndRelay(accounts, spool, "alice@acme.test", []string{"a@far.test"}, raw, time.Unix(1000, 0)); err != nil {
		t.Fatalf("send with no limit configured failed: %v", err)
	}
}

// TestApplyMessageSizeSettings covers the settings plumbing every daemon shares:
// no stored row leaves the current limit alone, a stored row applies at once.
func TestApplyMessageSizeSettings(t *testing.T) {
	SetMaxMessageSize(0)
	t.Cleanup(func() { SetMaxMessageSize(0) })

	ApplyMessageSizeSettings("test", func() (MessageSizeSettings, bool, error) {
		return MessageSizeSettings{MaxInboundBytes: 4096}, false, nil
	})
	if overMessageSize(make([]byte, 8192)) {
		t.Error("an unsaved settings row applied a limit")
	}

	ApplyMessageSizeSettings("test", func() (MessageSizeSettings, bool, error) {
		return MessageSizeSettings{MaxInboundBytes: 4096}, true, nil
	})
	if !overMessageSize(make([]byte, 8192)) {
		t.Error("the stored limit was not applied")
	}
	if overMessageSize(make([]byte, 4096)) {
		t.Error("a message exactly at the limit was refused")
	}
}
