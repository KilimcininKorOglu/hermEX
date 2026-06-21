package antispam

import (
	"net"
	"strings"
	"testing"
)

// TestTag proves the verdict is rendered into X-Spam headers prepended to the
// message, with the original body preserved.
func TestTag(t *testing.T) {
	orig := "Subject: hi\r\n\r\nbody"
	out := string(Tag([]byte(orig), Verdict{Score: 9, Spam: true}))
	if !strings.Contains(out, "X-Spam-Flag: YES") || !strings.Contains(out, "X-Spam-Score: 9") {
		t.Fatalf("missing spam headers: %q", out)
	}
	if !strings.HasSuffix(out, orig) {
		t.Errorf("original message not preserved: %q", out)
	}
}

// newTestScorer builds a Scorer with injected deterministic checks so the scoring
// logic is exercised without live DNS.
func newTestScorer(spfResult AuthResult, dkimValid bool) *Scorer {
	return &Scorer{
		Weights:   DefaultWeights,
		Threshold: 5,
		checkSPF:  func(net.IP, string, string) AuthResult { return spfResult },
		checkDKIM: func([]byte) []DKIMResult { return []DKIMResult{{Domain: "d", Valid: dkimValid}} },
	}
}

// TestScoreCleanMail proves an SPF-pass, DKIM-valid message scores zero and is ham.
func TestScoreCleanMail(t *testing.T) {
	v := newTestScorer(AuthPass, true).Score(Input{Raw: []byte("x"), ClientIP: net.IPv4(1, 2, 3, 4), MailFrom: "a@x"})
	if v.Spam || v.Score != 0 {
		t.Fatalf("clean mail = %+v, want score 0 not spam", v)
	}
	if v.SPF != AuthPass || v.DKIM != AuthPass {
		t.Errorf("auth = SPF %s DKIM %s, want both pass", v.SPF, v.DKIM)
	}
}

// TestScoreSPFFailAndNoDKIM proves failing authentication accumulates score past
// the threshold, flags spam, and records a reason per failed check.
func TestScoreSPFFailAndNoDKIM(t *testing.T) {
	v := newTestScorer(AuthFail, false).Score(Input{Raw: []byte("x"), ClientIP: net.IPv4(1, 2, 3, 4), MailFrom: "a@x"})
	want := DefaultWeights.SPFFail + DefaultWeights.DKIMFail
	if !v.Spam || v.Score != want {
		t.Fatalf("bad mail = %+v, want score %d and spam", v, want)
	}
	if len(v.Reasons) != 2 {
		t.Errorf("reasons = %v, want one for SPF and one for DKIM", v.Reasons)
	}
}

// TestScoreSoftFail proves a softfail contributes its (smaller) weight.
func TestScoreSoftFail(t *testing.T) {
	v := newTestScorer(AuthSoftFail, true).Score(Input{Raw: []byte("x"), ClientIP: net.IPv4(1, 2, 3, 4), MailFrom: "a@x"})
	if v.Score != DefaultWeights.SPFSoftFail {
		t.Fatalf("softfail score = %d, want %d", v.Score, DefaultWeights.SPFSoftFail)
	}
}

// TestScoreSkipsChecksWithoutInputs proves missing inputs skip the checks (so the
// real, DNS-hitting probes are never reached) and yield a clean, non-spam verdict.
func TestScoreSkipsChecksWithoutInputs(t *testing.T) {
	v := New(DefaultWeights, 5).Score(Input{})
	if v.Spam || v.Score != 0 || v.SPF != AuthNone || v.DKIM != AuthNone {
		t.Fatalf("empty input = %+v, want a clean none/none verdict", v)
	}
}

// TestNewWiresChecks proves the production constructor wires all real checks.
func TestNewWiresChecks(t *testing.T) {
	s := New(DefaultWeights, 5)
	if s.checkSPF == nil || s.checkDKIM == nil || s.lookupDMARC == nil {
		t.Fatal("New must wire the real SPF, DKIM, and DMARC checks")
	}
}

// TestScoreDMARCFailEnforced proves an unaligned message under an enforcing DMARC
// policy adds the DMARC weight and is flagged spam.
func TestScoreDMARCFailEnforced(t *testing.T) {
	s := &Scorer{
		Weights: DefaultWeights, Threshold: 5,
		checkSPF:    func(net.IP, string, string) AuthResult { return AuthFail },
		checkDKIM:   func([]byte) []DKIMResult { return nil },
		lookupDMARC: func(string) (string, bool) { return "reject", true },
	}
	v := s.Score(Input{Raw: []byte("x"), ClientIP: net.IPv4(1, 2, 3, 4), MailFrom: "a@evil.example", FromDomain: "bank.example"})
	if v.DMARC != AuthFail {
		t.Errorf("DMARC = %s, want fail", v.DMARC)
	}
	want := DefaultWeights.SPFFail + DefaultWeights.DKIMFail + DefaultWeights.DMARCFail
	if v.Score != want || !v.Spam {
		t.Fatalf("verdict = %+v, want score %d and spam", v, want)
	}
}

// TestScoreDMARCAlignedPass proves a DKIM signature on a subdomain of the From
// domain aligns (relaxed, organizational-domain) and makes DMARC pass even with
// no SPF.
func TestScoreDMARCAlignedPass(t *testing.T) {
	s := &Scorer{
		Weights: DefaultWeights, Threshold: 100,
		checkDKIM:   func([]byte) []DKIMResult { return []DKIMResult{{Domain: "mail.bank.example", Valid: true}} },
		lookupDMARC: func(string) (string, bool) { return "reject", true },
	}
	v := s.Score(Input{Raw: []byte("x"), FromDomain: "bank.example"})
	if v.DMARC != AuthPass {
		t.Errorf("DMARC = %s, want pass (DKIM aligned by organizational domain)", v.DMARC)
	}
}

// TestScoreDNSBLHit proves each blocklist zone listing the client IP adds the
// DNSBL weight and is recorded.
func TestScoreDNSBLHit(t *testing.T) {
	s := &Scorer{
		Weights: DefaultWeights, Threshold: DefaultThreshold,
		Zones:      []string{"zen.example", "bl.example"},
		checkDNSBL: func(ip net.IP, zone string) bool { return zone == "zen.example" },
	}
	v := s.Score(Input{ClientIP: net.IPv4(10, 0, 0, 1)})
	if len(v.DNSBL) != 1 || v.DNSBL[0] != "zen.example" {
		t.Fatalf("DNSBL = %v, want [zen.example]", v.DNSBL)
	}
	if v.Score != DefaultWeights.DNSBLHit {
		t.Errorf("score = %d, want %d", v.Score, DefaultWeights.DNSBLHit)
	}
}

// TestScoreDNSBLDormantWithoutZones proves no DNSBL lookup runs when no zones are
// configured, so the real DNS-hitting probe is never reached.
func TestScoreDNSBLDormantWithoutZones(t *testing.T) {
	called := false
	s := &Scorer{
		Weights: DefaultWeights, Threshold: DefaultThreshold,
		checkDNSBL: func(net.IP, string) bool { called = true; return true },
	}
	v := s.Score(Input{ClientIP: net.IPv4(10, 0, 0, 1)})
	if called {
		t.Error("DNSBL was checked with no zones configured")
	}
	if len(v.DNSBL) != 0 || v.Score != 0 {
		t.Errorf("verdict = %+v, want clean", v)
	}
}

// TestParseZones proves the comma-separated zone list parses and trims blanks.
func TestParseZones(t *testing.T) {
	got := ParseZones(" zen.example , , bl.example ")
	if len(got) != 2 || got[0] != "zen.example" || got[1] != "bl.example" {
		t.Errorf("ParseZones = %v, want [zen.example bl.example]", got)
	}
	if ParseZones("") != nil {
		t.Error(`ParseZones("") should be nil`)
	}
}

// TestDNSBLQuery proves the reversed-IP query name is built for IPv4 and IPv6.
func TestDNSBLQuery(t *testing.T) {
	if q := dnsblQuery(net.IPv4(1, 2, 3, 4), "zen.example"); q != "4.3.2.1.zen.example" {
		t.Errorf("IPv4 query = %q, want 4.3.2.1.zen.example", q)
	}
	want6 := "1.0." + strings.Repeat("0.0.", 15) + "z"
	if q := dnsblQuery(net.ParseIP("::1"), "z"); q != want6 {
		t.Errorf("IPv6 query = %q, want %q", q, want6)
	}
}

// saScoreRules is a tiny ruleset whose two rules together exceed saScoreThreshold
// (3.0 + 2.5 = 5.5) so a message hitting both makes the SA signal fire.
const saScoreRules = `
body   WIN_PRIZE  /win a prize/i
score  WIN_PRIZE  3.0
header SUBJ_URGENT  Subject =~ /urgent/i
score  SUBJ_URGENT  2.5
`

// TestScoreSARulesContributes proves that when the SpamAssassin rule subset's
// summed score crosses the threshold it adds one bounded weight (not the raw SA
// score) and records the score and the rules that fired.
func TestScoreSARulesContributes(t *testing.T) {
	s := &Scorer{Weights: DefaultWeights, Threshold: DefaultThreshold, SARules: ParseSARules(saScoreRules)}
	raw := []byte("Subject: URGENT notice\r\n\r\nYou win a prize today!\r\n")

	v := s.Score(Input{Raw: raw})
	if v.SAScore < 5.49 || v.SAScore > 5.51 {
		t.Errorf("SAScore = %v, want 5.5", v.SAScore)
	}
	if v.Score != DefaultWeights.SARulesHit {
		t.Errorf("score = %d, want the single bounded SA weight %d (not the raw SA score)", v.Score, DefaultWeights.SARulesHit)
	}
	if len(v.SAHits) != 2 {
		t.Errorf("SAHits = %v, want both rules", v.SAHits)
	}
}

// TestScoreSARulesBelowThreshold proves a sub-threshold SA score records the score
// and hits for transparency but contributes no weight.
func TestScoreSARulesBelowThreshold(t *testing.T) {
	s := &Scorer{Weights: DefaultWeights, Threshold: DefaultThreshold, SARules: ParseSARules(saScoreRules)}
	raw := []byte("Subject: hello\r\n\r\nYou win a prize today!\r\n") // only WIN_PRIZE (3.0)

	v := s.Score(Input{Raw: raw})
	if v.SAScore != 3.0 {
		t.Errorf("SAScore = %v, want 3.0", v.SAScore)
	}
	if v.Score != 0 {
		t.Errorf("score = %d, want 0 (3.0 is below the SA threshold)", v.Score)
	}
}

// TestScoreSARulesDormant proves no SA evaluation happens when no ruleset is set.
func TestScoreSARulesDormant(t *testing.T) {
	v := (&Scorer{Weights: DefaultWeights, Threshold: DefaultThreshold}).Score(Input{Raw: []byte("Subject: URGENT\r\n\r\nwin a prize")})
	if v.SAScore != 0 || v.SAHits != nil {
		t.Errorf("verdict = SAScore %v SAHits %v, want a dormant SA signal", v.SAScore, v.SAHits)
	}
}

// TestDNSBLIsListed proves only a 127.0.0.0/8 answer counts as a listing: a
// public A record (a hijacked or wildcard resolver) must not condemn the sender.
func TestDNSBLIsListed(t *testing.T) {
	if !isListed([]net.IP{net.IPv4(127, 0, 0, 2)}) {
		t.Error("127.0.0.2 must count as listed")
	}
	if isListed([]net.IP{net.IPv4(93, 184, 216, 34)}) {
		t.Error("a public A record must not count as listed")
	}
	if isListed(nil) {
		t.Error("no answer is not a listing")
	}
}
