package antispam

import (
	"net"
	"testing"
)

// TestAccessListAction is the precedence and matching teeth: an exact address beats
// a domain, the From-header domain is matched at the domain tier, exact-domain does
// not cover a subdomain, and an empty envelope sender matches nothing.
func TestAccessListAction(t *testing.T) {
	a := NewAccessList(map[string]string{
		"vip@example.com": AccessAllow,
		"example.com":     AccessBlock,
		"blocked.example": AccessBlock,
	})
	cases := []struct {
		mailFrom, fromDomain, want string
	}{
		{"vip@example.com", "", AccessAllow},                // exact beats the domain block
		{"VIP@Example.com", "", AccessAllow},                // case-insensitive
		{"other@example.com", "", AccessBlock},              // domain rule applies to non-exact
		{"a@clean.example", "blocked.example", AccessBlock}, // From-header domain, domain tier
		{"a@clean.example", "", ""},                         // no rule
		{"", "example.com", ""},                             // empty envelope: a bounce matches nothing
		{"a@sub.example.com", "", ""},                       // exact-domain only: subdomain not covered
	}
	for _, c := range cases {
		if got := a.Action(c.mailFrom, c.fromDomain); got != c.want {
			t.Errorf("Action(%q, %q) = %q, want %q", c.mailFrom, c.fromDomain, got, c.want)
		}
	}
}

// TestAccessBlockForcesSpam proves a blocklisted sender is filed as spam even when
// the message scores clean.
func TestAccessBlockForcesSpam(t *testing.T) {
	s := &Scorer{}
	s.SetConfig(&Config{Weights: DefaultWeights, Threshold: 100}) // would be ham on score
	s.SetAccess(NewAccessList(map[string]string{"spammer@evil.example": AccessBlock}))
	v := s.Score(Input{Raw: []byte("x"), MailFrom: "spammer@evil.example"})
	if !v.Spam {
		t.Errorf("blocklisted sender must be spam, got %+v", v)
	}
}

// TestAccessAllowRescuesFromScore proves an allowlisted sender is rescued from
// score-based junking.
func TestAccessAllowRescuesFromScore(t *testing.T) {
	s := &Scorer{checkSPF: func(net.IP, string, string) AuthResult { return AuthFail }}
	s.SetConfig(&Config{Weights: DefaultWeights, Threshold: 1}) // SPFFail (5) >= 1 -> would be spam
	s.SetAccess(NewAccessList(map[string]string{"friend@partner.example": AccessAllow}))
	v := s.Score(Input{Raw: []byte("x"), ClientIP: net.IPv4(1, 2, 3, 4), MailFrom: "friend@partner.example"})
	if v.Spam {
		t.Errorf("allowlisted sender must be rescued from score-based junking, got %+v", v)
	}
}

// TestAccessAllowDoesNotOverrideDMARCReject is the spoofing teeth: an allowlisted
// domain must NOT rescue a message that hard-fails DMARC under an enforcing policy,
// or anyone spoofing the allowlisted domain would bypass authentication.
func TestAccessAllowDoesNotOverrideDMARCReject(t *testing.T) {
	s := &Scorer{
		checkSPF:    func(net.IP, string, string) AuthResult { return AuthFail },
		checkDKIM:   func([]byte) []DKIMResult { return nil },
		lookupDMARC: func(string) (string, bool) { return "reject", true },
	}
	s.SetConfig(&Config{Weights: DefaultWeights, Threshold: 1})
	s.SetAccess(NewAccessList(map[string]string{"partner.example": AccessAllow}))

	// Spoofer: From-header claims the allowlisted domain, envelope is elsewhere, so
	// DMARC does not align and the published reject policy makes it a hard failure.
	v := s.Score(Input{
		Raw: []byte("x"), ClientIP: net.IPv4(1, 2, 3, 4),
		MailFrom: "attacker@evil.example", FromDomain: "partner.example",
	})
	if v.DMARC != AuthFail {
		t.Fatalf("expected a DMARC failure to set up the test, got %s", v.DMARC)
	}
	if !v.Spam {
		t.Errorf("an allowlisted domain must NOT rescue a hard DMARC failure (spoofing), got %+v", v)
	}
}

// TestAccessExactBeatsDomainOverride proves the override honours precedence: an
// exact-address allow rescues even when its domain is blocklisted.
func TestAccessExactBeatsDomainOverride(t *testing.T) {
	s := &Scorer{}
	s.SetConfig(&Config{Weights: DefaultWeights, Threshold: 100})
	s.SetAccess(NewAccessList(map[string]string{
		"example.com":     AccessBlock,
		"vip@example.com": AccessAllow,
	}))
	if v := s.Score(Input{Raw: []byte("x"), MailFrom: "vip@example.com"}); v.Spam {
		t.Errorf("exact allow must beat the domain block, got %+v", v)
	}
	if v := s.Score(Input{Raw: []byte("x"), MailFrom: "random@example.com"}); !v.Spam {
		t.Errorf("domain block must apply to a non-exact address, got %+v", v)
	}
}

// TestAccessEmptyMailFromNotOverridden proves a bounce (empty envelope sender) is
// never matched, even when its From-header domain is blocklisted.
func TestAccessEmptyMailFromNotOverridden(t *testing.T) {
	s := &Scorer{}
	s.SetConfig(&Config{Weights: DefaultWeights, Threshold: 100})
	s.SetAccess(NewAccessList(map[string]string{"example.com": AccessBlock}))
	if v := s.Score(Input{Raw: []byte("x"), MailFrom: "", FromDomain: "example.com"}); v.Spam {
		t.Errorf("empty MailFrom (a bounce) must not be overridden, got %+v", v)
	}
	if v := s.Score(Input{Raw: []byte("x"), MailFrom: "", FromDomain: "example.com"}); v.AccessMatched {
		t.Errorf("empty MailFrom (a bounce) must not mark AccessMatched, got %+v", v)
	}
}

// TestAccessMatchedFlag proves the verdict reports whether an operator rule decided
// it: set for a blocklisted or allowlisted sender (so delivery treats Spam as
// authoritative for every recipient), and clear for a purely score-driven verdict
// (so delivery may re-evaluate it against a per-recipient threshold).
func TestAccessMatchedFlag(t *testing.T) {
	s := &Scorer{}
	s.SetConfig(&Config{Weights: DefaultWeights, Threshold: 100})
	s.SetAccess(NewAccessList(map[string]string{
		"blocked@evil.example": AccessBlock,
		"vip@partner.example":  AccessAllow,
	}))

	if v := s.Score(Input{Raw: []byte("x"), MailFrom: "blocked@evil.example"}); !v.AccessMatched {
		t.Errorf("a blocklisted sender must mark AccessMatched, got %+v", v)
	}
	if v := s.Score(Input{Raw: []byte("x"), MailFrom: "vip@partner.example"}); !v.AccessMatched {
		t.Errorf("an allowlisted sender must mark AccessMatched, got %+v", v)
	}
	if v := s.Score(Input{Raw: []byte("x"), MailFrom: "nobody@neutral.example"}); v.AccessMatched {
		t.Errorf("a sender with no access rule must not mark AccessMatched, got %+v", v)
	}
}
