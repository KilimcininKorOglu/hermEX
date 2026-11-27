package mta

import (
	"errors"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/relay"
	"hermex/internal/smtp"
)

// forwardingDir is a hosted-local directory whose users carry forward settings.
type forwardingDir struct {
	hostedLocal
	forwards map[string]directory.ForwardInfo
	broken   bool
}

func (d forwardingDir) GetForward(addr string) (directory.ForwardInfo, bool, error) {
	if d.broken {
		return directory.ForwardInfo{}, false, errors.New("directory down")
	}
	fi, ok := d.forwards[addr]
	return fi, ok, nil
}

// forwardSession is an unauthenticated inbound session over a directory with
// alice@local and carol@local and the given forwards.
func forwardSession(t *testing.T, forwards map[string]directory.ForwardInfo, withSpool bool) *session {
	t.Helper()
	s := srsSession(t, "x@remote.test")
	if !withSpool {
		s.spool = nil
	}
	s.accounts = forwardingDir{hostedLocal: hostedLocal{directory.StaticAccounts{
		"alice@local": {MailboxPath: t.TempDir()}, "carol@local": {MailboxPath: t.TempDir()},
	}}, forwards: forwards}
	return s
}

// routed lists the local targets and the external forwards of a session.
func routed(s *session) (locals []string, forwards map[string][]string) {
	for _, tg := range s.targets {
		locals = append(locals, tg.addr)
	}
	forwards = map[string][]string{}
	for _, g := range s.forwards {
		for _, r := range g.rcpts {
			forwards[g.domain] = append(forwards[g.domain], r.Addr)
		}
	}
	return locals, forwards
}

// rcptAlice routes alice@local through a session with the given forwards.
func rcptAlice(t *testing.T, forwards map[string]directory.ForwardInfo) ([]string, map[string][]string) {
	t.Helper()
	s := forwardSession(t, forwards, true)
	if err := s.Rcpt("alice@local", smtp.RcptParams{}); err != nil {
		t.Fatal(err)
	}
	return routed(s)
}

// TestInboundRedirectLeavesOnlyTheDestination: mail from the internet to a
// user who redirects goes only to the destination, as a forward of the user's
// domain, so it gets that domain's SRS envelope and ARC seal.
func TestInboundRedirectLeavesOnlyTheDestination(t *testing.T) {
	locals, fw := rcptAlice(t, map[string]directory.ForwardInfo{
		"alice@local": {Type: directory.ForwardRedirect, Destination: "Out@Remote.test"}})
	if len(locals) != 0 || len(fw["local"]) != 1 || fw["local"][0] != "out@remote.test" {
		t.Errorf("local %v, forwards %v; want none and out@remote.test under local", locals, fw)
	}
}

// TestInboundCCForwardKeepsTheLocalCopy: a CC forward delivers locally and to
// the destination.
func TestInboundCCForwardKeepsTheLocalCopy(t *testing.T) {
	locals, fw := rcptAlice(t, map[string]directory.ForwardInfo{
		"alice@local": {Type: directory.ForwardCC, Destination: "out@remote.test"}})
	if len(locals) != 1 || len(fw["local"]) != 1 {
		t.Errorf("local %v, forwards %v; want alice@local and the destination", locals, fw)
	}
}

// TestInboundForwardIsOneHop: a local destination is filed directly without
// applying its own forward, so two users forwarding to each other cannot loop.
func TestInboundForwardIsOneHop(t *testing.T) {
	locals, fw := rcptAlice(t, map[string]directory.ForwardInfo{
		"alice@local": {Type: directory.ForwardRedirect, Destination: "carol@local"},
		"carol@local": {Type: directory.ForwardRedirect, Destination: "alice@local"},
	})
	if len(locals) != 1 || locals[0] != "carol@local" || len(fw) != 0 {
		t.Errorf("local %v, forwards %v; want only carol@local", locals, fw)
	}
}

// TestForwardThatCannotLeaveKeepsTheMail: a redirect whose destination cannot
// be relayed from here, or a forward setting the directory cannot read, files
// the message in the user's own mailbox instead of dropping it.
func TestForwardThatCannotLeaveKeepsTheMail(t *testing.T) {
	ext := map[string]directory.ForwardInfo{"alice@local": {Type: directory.ForwardRedirect, Destination: "out@remote.test"}}
	s := forwardSession(t, ext, false)
	mustNoErr(t, "Rcpt without spool", s.Rcpt("alice@local", smtp.RcptParams{}))
	locals, fw := routed(s)
	wantEq(t, "no-spool locals", strings.Join(locals, ","), "alice@local")
	wantEq(t, "no-spool forwards", len(fw), 0)

	s = forwardSession(t, ext, true)
	d, ok := s.accounts.(forwardingDir)
	if !ok {
		t.Fatalf("accounts type = %T, want forwardingDir", s.accounts)
	}
	d.broken = true
	s.accounts = d
	mustNoErr(t, "Rcpt with broken forward lookup", s.Rcpt("alice@local", smtp.RcptParams{}))
	locals, fw = routed(s)
	wantEq(t, "unreadable locals", len(locals), 1)
	wantEq(t, "unreadable forwards", len(fw), 0)

	// Mixed-case self-forward delivers locally without forwarding loop.
	locals, fw = rcptAlice(t, map[string]directory.ForwardInfo{
		"alice@local": {Type: directory.ForwardRedirect, Destination: "ALICE@LOCAL"},
	})
	wantEq(t, "self-forward locals", strings.Join(locals, ","), "alice@local")
	wantEq(t, "self-forward forwards", len(fw), 0)
}

// TestAuthenticatedForwardIsAuthorSigned: a submission's From passed sendas, so
// the forwarded copy joins the author-signed relay targets, not the sealed
// forwards, and an outbound-limited owner defers without adding targets.
func TestAuthenticatedForwardIsAuthorSigned(t *testing.T) {
	ext := map[string]directory.ForwardInfo{"alice@local": {Type: directory.ForwardRedirect, Destination: "out@remote.test"}}
	s := forwardSession(t, ext, true)
	s.authUser = "carol@local"
	if err := s.Rcpt("alice@local", smtp.RcptParams{}); err != nil {
		t.Fatal(err)
	}
	if len(s.forwards) != 0 || len(s.relayTargets) != 1 || s.relayTargets[0] != (relay.DSNRecipient{Addr: "out@remote.test"}) {
		t.Errorf("forwards %v, relay targets %v; want the destination as an author-signed relay", s.forwards, s.relayTargets)
	}

	ob := NewOutboundLimiter()
	ob.SetLimits(1, time.Hour)
	ob.SetEnabled(true)
	if !ob.Allow("alice@local") {
		t.Fatal("first allow failed")
	}
	sLim := forwardSession(t, ext, true)
	sLim.authUser = "carol@local"
	sLim.outbound = ob
	err := sLim.Rcpt("alice@local", smtp.RcptParams{})
	if _, ok := errors.AsType[*smtp.TempError](err); !ok {
		t.Fatalf("outbound-limited forward err = %v, want *smtp.TempError", err)
	}
	wantEq(t, "forwards on outbound limit", len(sLim.forwards), 0)
	wantEq(t, "relayTargets on outbound limit", len(sLim.relayTargets), 0)
}
