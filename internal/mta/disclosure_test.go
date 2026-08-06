package mta

import (
	"errors"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/smtp"
)

// driverFailure is the shape of a directory outage reaching the routing path: the
// unwrapped driver error, which names database internals.
var driverFailure = errors.New("dial tcp 10.0.0.5:3306: connect: connection refused")

// brokenDomains is a directory that resolves nothing and whose domain lookup fails,
// the state during a database outage.
type brokenDomains struct{ directory.StaticAccounts }

func (brokenDomains) IsLocalDomain(string) (bool, error) { return false, driverFailure }

// TestRoutingFailureDefersInsteadOfRejecting proves a directory outage answers with a
// temporary failure. A permanent rejection would make the sending MTA bounce mail
// this server can accept once the database is back, so every message arriving during
// an outage would be lost.
func TestRoutingFailureDefersInsteadOfRejecting(t *testing.T) {
	s := &session{accounts: brokenDomains{directory.StaticAccounts{}}, authUser: "alice@local"}

	err := s.Rcpt("bob@elsewhere.test", smtp.RcptParams{})
	if err == nil {
		t.Fatal("a routing failure was accepted")
	}
	if _, ok := errors.AsType[*smtp.TempError](err); !ok {
		t.Errorf("error is %T (%v), want *smtp.TempError so the sender retries", err, err)
	}
}

// TestRoutingFailureWithholdsTheDriverError proves the deferral message carries no
// database detail. Port 25 is unauthenticated, so the message is written to an
// untrusted peer.
func TestRoutingFailureWithholdsTheDriverError(t *testing.T) {
	s := &session{accounts: brokenDomains{directory.StaticAccounts{}}, authUser: "alice@local"}

	err := s.Rcpt("bob@elsewhere.test", smtp.RcptParams{})
	if err == nil {
		t.Fatal("a routing failure was accepted")
	}
	for _, leak := range []string{"10.0.0.5", "3306", "connection refused"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the reply message carries driver detail %q: %q", leak, err)
		}
	}
}

// TestUnknownRecipientStaysPermanent proves the classification did not turn every
// rejection into a deferral: a genuine user-unknown is still permanent, and still
// names the address, which is the line a bounce quotes back to the sender.
func TestUnknownRecipientStaysPermanent(t *testing.T) {
	s := &session{accounts: directory.StaticAccounts{}, authUser: "alice@local"}

	err := s.Rcpt("bob@local", smtp.RcptParams{})
	if err == nil {
		t.Fatal("an unknown recipient was accepted")
	}
	pe, ok := errors.AsType[*smtp.PermError](err)
	if !ok {
		t.Fatalf("error is %T (%v), want *smtp.PermError", err, err)
	}
	if !strings.Contains(pe.Message, "bob@local") {
		t.Errorf("message = %q, want it to name the unknown recipient", pe.Message)
	}
}

// TestRelayDenialStaysPermanent proves an unauthenticated relay attempt is still a
// permanent refusal, so an open-relay probe is not invited to retry.
func TestRelayDenialStaysPermanent(t *testing.T) {
	s := &session{accounts: directory.StaticAccounts{}}

	err := s.Rcpt("bob@elsewhere.test", smtp.RcptParams{})
	if err == nil {
		t.Fatal("relay to an unknown recipient was accepted")
	}
	if _, ok := errors.AsType[*smtp.PermError](err); !ok {
		t.Errorf("error is %T (%v), want *smtp.PermError", err, err)
	}
}
