package mta

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/smtp"
)

// catchAllAccounts is a directory whose domain collects unknown recipients in one mailbox.
// It answers ResolveCatchAll for that domain only, which is what the delivery path asks.
type catchAllAccounts struct {
	directory.StaticAccounts
	domain  string
	mailbox string
}

func (c catchAllAccounts) ResolveCatchAll(address string) (string, bool) {
	if c.mailbox == "" || !strings.HasSuffix(strings.ToLower(address), "@"+c.domain) {
		return "", false
	}
	return c.mailbox, true
}

// catchAllSession builds a session for an unauthenticated inbound sender, the case a
// catch-all exists for, with the given catch-all mailbox ("" for none).
func catchAllSession(t *testing.T, catchMailbox string) (*session, string) {
	t.Helper()
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := catchAllAccounts{
		StaticAccounts: directory.StaticAccounts{"alice@local": {Password: "pw", MailboxPath: mbox}},
		domain:         "local",
		mailbox:        catchMailbox,
	}
	s := &session{accounts: accounts}
	mustNoErr(t, "MAIL FROM", s.Mail("sender@remote", smtp.MailParams{}))
	return s, mbox
}

// TestCatchAllAcceptsAnUnknownRecipient is the load-bearing case: with a catch-all
// configured, a local part no account owns is accepted instead of refused.
func TestCatchAllAcceptsAnUnknownRecipient(t *testing.T) {
	catchMbox := filepath.Join(t.TempDir(), "catchall")
	s, _ := catchAllSession(t, catchMbox)

	mustNoErr(t, "RCPT to an unknown local part", s.Rcpt("nosuchuser@local", smtp.RcptParams{}))

	if len(s.targets) != 1 || s.targets[0].path != catchMbox {
		t.Errorf("targets = %+v, want one filed into the catch-all mailbox", s.targets)
	}
}

// TestCatchAllDeliversTheMessage proves the accepted message reaches the catch-all mailbox.
func TestCatchAllDeliversTheMessage(t *testing.T) {
	catchMbox := filepath.Join(t.TempDir(), "catchall")
	s, _ := catchAllSession(t, catchMbox)
	mustNoErr(t, "RCPT", s.Rcpt("nosuchuser@local", smtp.RcptParams{}))

	mustNoErr(t, "DATA", s.Data(bytes.NewReader([]byte("Subject: hi\r\n\r\nhello\r\n"))))

	wantEq(t, "messages in the catch-all inbox", len(inboxMessages(t, catchMbox)), 1)
}

// TestCatchAllRecordsTheEnvelopeRecipient proves the delivered message names the address it
// was sent to, which owns no account and need not appear in any header the sender wrote.
func TestCatchAllRecordsTheEnvelopeRecipient(t *testing.T) {
	catchMbox := filepath.Join(t.TempDir(), "catchall")
	s, _ := catchAllSession(t, catchMbox)
	mustNoErr(t, "RCPT", s.Rcpt("nosuchuser@local", smtp.RcptParams{}))
	mustNoErr(t, "DATA", s.Data(bytes.NewReader([]byte("Subject: hi\r\n\r\nhello\r\n"))))

	raw := inboxRaw(t, catchMbox, 1)

	if !bytes.Contains(raw, []byte("Delivered-To: nosuchuser@local")) {
		t.Errorf("the delivered message does not name the envelope recipient:\n%s", raw)
	}
}

// TestCatchAllLeavesAKnownRecipientUnmarked proves an ordinary delivery is unchanged, so
// the header marks exactly the deliveries whose address would otherwise be lost.
func TestCatchAllLeavesAKnownRecipientUnmarked(t *testing.T) {
	catchMbox := filepath.Join(t.TempDir(), "catchall")
	s, mbox := catchAllSession(t, catchMbox)
	mustNoErr(t, "RCPT", s.Rcpt("alice@local", smtp.RcptParams{}))
	mustNoErr(t, "DATA", s.Data(bytes.NewReader([]byte("Subject: hi\r\n\r\nhello\r\n"))))

	raw := inboxRaw(t, mbox, 1)

	if bytes.Contains(raw, []byte("Delivered-To:")) {
		t.Errorf("an ordinary delivery was marked as a catch-all one:\n%s", raw)
	}
}

// TestWithoutCatchAllTheUnknownRecipientIsStillRefused proves the setting is off by
// default: a domain with no catch-all keeps refusing an unknown local part.
func TestWithoutCatchAllTheUnknownRecipientIsStillRefused(t *testing.T) {
	s, _ := catchAllSession(t, "")

	if err := s.Rcpt("nosuchuser@local", smtp.RcptParams{}); err == nil {
		t.Error("an unknown local part must be refused while the domain has no catch-all")
	}
}

// TestCatchAllDoesNotMakeAForeignAddressLocal proves the fallback is per domain: an address
// in a domain this server does not host stays refused for an unauthenticated sender.
func TestCatchAllDoesNotMakeAForeignAddressLocal(t *testing.T) {
	catchMbox := filepath.Join(t.TempDir(), "catchall")
	s, _ := catchAllSession(t, catchMbox)

	if err := s.Rcpt("someone@remote", smtp.RcptParams{}); err == nil {
		t.Error("an address in another domain must not reach this domain's catch-all")
	}
}
