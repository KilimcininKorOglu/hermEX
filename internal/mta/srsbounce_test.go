package mta

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/relay"
	"hermex/internal/smtp"
	"hermex/internal/srs"
)

// hostedLocal is a directory whose only hosted domain is local.
type hostedLocal struct{ directory.StaticAccounts }

func (hostedLocal) IsLocalDomain(d string) (bool, error) { return strings.EqualFold(d, "local"), nil }

// srsSession is an unauthenticated session from sender with a spool and the
// test Forwarding's SRS secret.
func srsSession(t *testing.T, sender string) *session {
	t.Helper()
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sp.Close(); err != nil {
			t.Error(err)
		}
	})
	accounts := hostedLocal{directory.StaticAccounts{"alice@local": {MailboxPath: t.TempDir()}}}
	return &session{accounts: accounts, from: sender, spool: sp, forwarding: testForwarding(t)}
}

// TestBounceToARewrittenSenderReachesTheOriginal sends a delivery status
// notification (null reverse-path) to the SRS address a forward carried. It is
// relayed to the original sender, as an external recipient or a local mailbox.
func TestBounceToARewrittenSenderReachesTheOriginal(t *testing.T) {
	s := srsSession(t, "")
	secret := s.forwarding.SRSSecret
	if err := s.Rcpt(srs.Forward("bob@remote.test", "local", secret, time.Now()), smtp.RcptParams{}); err != nil {
		t.Fatal(err)
	}
	if len(s.relayTargets) != 1 || s.relayTargets[0].Addr != "bob@remote.test" {
		t.Errorf("relay targets = %+v, want the original sender", s.relayTargets)
	}
	if err := s.Rcpt(srs.Forward("alice@local", "local", secret, time.Now()), smtp.RcptParams{}); err != nil {
		t.Fatal(err)
	}
	if len(s.targets) != 1 || s.targets[0].addr != "alice@local" {
		t.Errorf("local targets = %+v, want alice@local", s.targets)
	}
}

// TestRewrittenAddressIsNotAnOpenRelay covers what an outsider can send to a
// hosted SRS address: ordinary mail with a sender, a made-up hash, and an
// address minted longer ago than srs.MaxAge. Each is refused with a permanent
// error and nothing is relayed.
func TestRewrittenAddressIsNotAnOpenRelay(t *testing.T) {
	probe := srsSession(t, "")
	secret := probe.forwarding.SRSSecret
	good := srs.Forward("victim@remote.test", "local", secret, time.Now())
	cases := map[string]struct{ sender, rcpt string }{
		"ordinary mail": {"spammer@remote.test", good},
		"forged hash":   {"", "SRS0=AAAAAAAA" + good[strings.Index(good, "=")+9:]},
		"expired":       {"", srs.Forward("victim@remote.test", "local", secret, time.Now().Add(-srs.MaxAge-48*time.Hour))},
	}
	for name, c := range cases {
		s := srsSession(t, c.sender)
		var perm *smtp.PermError
		if err := s.Rcpt(c.rcpt, smtp.RcptParams{}); !errors.As(err, &perm) {
			t.Errorf("%s: RCPT %s answered %v, want a permanent refusal", name, c.rcpt, err)
		}
		if len(s.relayTargets) != 0 || len(s.targets) != 0 {
			t.Errorf("%s: the message was routed", name)
		}
	}
}
