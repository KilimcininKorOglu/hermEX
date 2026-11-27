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

type catchAllRig struct {
	sess      *session
	aliceMbox string
}

// catchAllSession builds a session for an unauthenticated inbound sender, the case a
// catch-all exists for, with the given catch-all mailbox ("" for none).
func catchAllSession(t *testing.T, catchMailbox string) catchAllRig {
	t.Helper()
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := catchAllAccounts{
		StaticAccounts: directory.StaticAccounts{"alice@local": {Password: "pw", MailboxPath: mbox}},
		domain:         "local",
		mailbox:        catchMailbox,
	}
	s := &session{accounts: accounts}
	mustNoErr(t, "MAIL FROM", s.Mail("sender@remote", smtp.MailParams{}))
	return catchAllRig{sess: s, aliceMbox: mbox}
}

// TestCatchAllAcceptsAnUnknownRecipient is the load-bearing case: with a catch-all
// configured, a local part no account owns is accepted instead of refused.
func TestCatchAllAcceptsAnUnknownRecipient(t *testing.T) {
	catchMbox := filepath.Join(t.TempDir(), "catchall")
	rig := catchAllSession(t, catchMbox)

	mustNoErr(t, "RCPT to an unknown local part", rig.sess.Rcpt("nosuchuser@local", smtp.RcptParams{}))

	if len(rig.sess.targets) != 1 || rig.sess.targets[0].path != catchMbox {
		t.Errorf("targets = %+v, want one filed into the catch-all mailbox", rig.sess.targets)
	}
}

// TestCatchAllDeliversTheMessage proves the accepted message reaches the catch-all mailbox.
func TestCatchAllDeliversTheMessage(t *testing.T) {
	catchMbox := filepath.Join(t.TempDir(), "catchall")
	rig := catchAllSession(t, catchMbox)
	mustNoErr(t, "RCPT", rig.sess.Rcpt("nosuchuser@local", smtp.RcptParams{}))

	mustNoErr(t, "DATA", rig.sess.Data(bytes.NewReader([]byte("Subject: hi\r\n\r\nhello\r\n"))))

	wantEq(t, "messages in the catch-all inbox", len(inboxMessages(t, catchMbox)), 1)
}

// TestCatchAllRecordsTheEnvelopeRecipient proves the delivered message names the address it
// was sent to, which owns no account and need not appear in any header the sender wrote,
// and omits Delivered-To when the envelope address contains control characters.
func TestCatchAllRecordsTheEnvelopeRecipient(t *testing.T) {
	catchMbox := filepath.Join(t.TempDir(), "catchall")
	rig := catchAllSession(t, catchMbox)
	mustNoErr(t, "RCPT", rig.sess.Rcpt("nosuchuser@local", smtp.RcptParams{}))
	mustNoErr(t, "DATA", rig.sess.Data(bytes.NewReader([]byte("Subject: hi\r\n\r\nhello\r\n"))))

	raw := inboxRaw(t, catchMbox, 1)

	if !bytes.Contains(raw, []byte("Delivered-To: nosuchuser@local")) {
		t.Errorf("the delivered message does not name the envelope recipient:\n%s", raw)
	}

	plain := []byte("Subject: hi\r\n\r\nhello\r\n")
	if got := markCatchAll(target{addr: "bad\r\nBcc: x@y", catchAll: true}, plain); !bytes.Equal(got, plain) {
		t.Errorf("control-byte catch-all address wrote header:\n%s", got)
	}
}

// TestCatchAllLeavesAKnownRecipientUnmarked proves an ordinary delivery is unchanged, so
// the header marks exactly the deliveries whose address would otherwise be lost.
func TestCatchAllLeavesAKnownRecipientUnmarked(t *testing.T) {
	catchMbox := filepath.Join(t.TempDir(), "catchall")
	rig := catchAllSession(t, catchMbox)
	mustNoErr(t, "RCPT", rig.sess.Rcpt("alice@local", smtp.RcptParams{}))
	mustNoErr(t, "DATA", rig.sess.Data(bytes.NewReader([]byte("Subject: hi\r\n\r\nhello\r\n"))))

	raw := inboxRaw(t, rig.aliceMbox, 1)

	if bytes.Contains(raw, []byte("Delivered-To:")) {
		t.Errorf("an ordinary delivery was marked as a catch-all one:\n%s", raw)
	}
}

// TestWithoutCatchAllTheUnknownRecipientIsStillRefused proves the setting is off by
// default: a domain with no catch-all keeps refusing an unknown local part.
func TestWithoutCatchAllTheUnknownRecipientIsStillRefused(t *testing.T) {
	rig := catchAllSession(t, "")

	if err := rig.sess.Rcpt("nosuchuser@local", smtp.RcptParams{}); err == nil {
		t.Error("an unknown local part must be refused while the domain has no catch-all")
	}
	wantEq(t, "targets after refusal", len(rig.sess.targets), 0)
	wantEq(t, "relayTargets after refusal", len(rig.sess.relayTargets), 0)
}

// TestCatchAllDoesNotMakeAForeignAddressLocal proves the fallback is per domain: an address
// in a domain this server does not host stays refused for an unauthenticated sender.
func TestCatchAllDoesNotMakeAForeignAddressLocal(t *testing.T) {
	catchMbox := filepath.Join(t.TempDir(), "catchall")
	rig := catchAllSession(t, catchMbox)

	if err := rig.sess.Rcpt("someone@remote", smtp.RcptParams{}); err == nil {
		t.Error("an address in another domain must not reach this domain's catch-all")
	}
	wantEq(t, "targets after foreign refusal", len(rig.sess.targets), 0)
	wantEq(t, "relayTargets after foreign refusal", len(rig.sess.relayTargets), 0)
}
