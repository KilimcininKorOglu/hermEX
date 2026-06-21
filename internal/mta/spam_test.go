package mta

import (
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/antispam"
	"hermex/internal/directory"
)

// recordingScorer captures the input it was asked to score and returns a fixed
// verdict, so a test can assert what the MTA fed the scorer and how often.
type recordingScorer struct {
	verdict antispam.Verdict
	calls   int
	last    antispam.Input
}

func (r *recordingScorer) Score(in antispam.Input) antispam.Verdict {
	r.calls++
	r.last = in
	return r.verdict
}

// TestInboundSpamScoring proves inbound mail is scored exactly once with the
// connection's client IP, envelope sender, and From-header domain.
func TestInboundSpamScoring(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	rec := &recordingScorer{verdict: antispam.Verdict{Score: 99, Spam: true}}
	b := &Backend{Accounts: accounts, Scorer: rec}

	sess, err := b.NewSession("203.0.113.9:1234")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Mail("bob@external.example"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Rcpt("alice@test"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Data(strings.NewReader("From: Bob <bob@external.example>\r\nSubject: hi\r\n\r\nbody")); err != nil {
		t.Fatal(err)
	}

	if rec.calls != 1 {
		t.Fatalf("scorer calls = %d, want 1", rec.calls)
	}
	if rec.last.MailFrom != "bob@external.example" {
		t.Errorf("scored MailFrom = %q, want bob@external.example", rec.last.MailFrom)
	}
	if rec.last.FromDomain != "external.example" {
		t.Errorf("scored FromDomain = %q, want external.example", rec.last.FromDomain)
	}
	if ip := rec.last.ClientIP; ip == nil || ip.String() != "203.0.113.9" {
		t.Errorf("scored ClientIP = %v, want 203.0.113.9", ip)
	}
}

// TestAuthenticatedSubmissionNotScored proves the user's own outbound (an
// authenticated submission) is not spam-scanned.
func TestAuthenticatedSubmissionNotScored(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	rec := &recordingScorer{}
	b := &Backend{Accounts: accounts, Scorer: rec}

	sess, err := b.NewSession("1.2.3.4:5")
	if err != nil {
		t.Fatal(err)
	}
	s := sess.(*session)
	s.authUser = "alice@test" // simulate a logged-in submitter
	s.from = "alice@test"
	s.targets = []target{{addr: "alice@test", path: mbox}}
	if err := s.Data(strings.NewReader("From: alice@test\r\nSubject: x\r\n\r\nbody")); err != nil {
		t.Fatal(err)
	}
	if rec.calls != 0 {
		t.Errorf("authenticated submission was scored (calls=%d), want 0", rec.calls)
	}
}
