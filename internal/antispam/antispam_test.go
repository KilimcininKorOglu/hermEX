package antispam

import (
	"net"
	"testing"
)

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

// TestNewWiresChecks proves the production constructor wires both real checks.
func TestNewWiresChecks(t *testing.T) {
	s := New(DefaultWeights, 5)
	if s.checkSPF == nil || s.checkDKIM == nil {
		t.Fatal("New must wire the real SPF and DKIM checks")
	}
}
