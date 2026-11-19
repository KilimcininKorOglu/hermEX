package mta

import (
	"errors"
	"testing"

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
	if err := s.Rcpt("alice@local", smtp.RcptParams{}); err != nil {
		t.Fatal(err)
	}
	if locals, fw := routed(s); len(locals) != 1 || locals[0] != "alice@local" || len(fw) != 0 {
		t.Errorf("no spool: local %v, forwards %v; want alice@local kept", locals, fw)
	}

	s = forwardSession(t, ext, true)
	d := s.accounts.(forwardingDir)
	d.broken = true
	s.accounts = d
	if err := s.Rcpt("alice@local", smtp.RcptParams{}); err != nil {
		t.Fatal(err)
	}
	if locals, fw := routed(s); len(locals) != 1 || len(fw) != 0 {
		t.Errorf("unreadable setting: local %v, forwards %v; want alice@local kept", locals, fw)
	}
}

// TestAuthenticatedForwardIsAuthorSigned: a submission's From passed sendas, so
// the forwarded copy joins the author-signed relay targets, not the sealed
// forwards.
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
}
