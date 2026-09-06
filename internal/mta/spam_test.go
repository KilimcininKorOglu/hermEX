package mta

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/antispam"
	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/smtp"
)

// recordingHistory captures recorded verdicts; errHistory always fails, to prove
// the recorder is fail-open.
type recordingHistory struct{ records []directory.SpamVerdict }

func (r *recordingHistory) RecordSpamVerdict(v directory.SpamVerdict) error {
	r.records = append(r.records, v)
	return nil
}

type errHistory struct{}

func (errHistory) RecordSpamVerdict(directory.SpamVerdict) error { return errors.New("db down") }

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
	if err := sess.Mail("bob@external.example", smtp.MailParams{}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Rcpt("alice@test", smtp.RcptParams{}); err != nil {
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

// TestSpamScoredLogsReasons proves the spam.scored log event carries the joined
// verdict reasons, so an admin can debug a false positive from the log viewer.
func TestSpamScoredLogsReasons(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	sink := &captureSink{}
	rec := &recordingScorer{verdict: antispam.Verdict{Score: 7, Spam: false, Reasons: []string{"SPF fail", "Bayesian: likely spam"}}}
	b := &Backend{Accounts: accounts, Scorer: rec, Logger: logging.New(sink)}

	sess, err := b.NewSession("203.0.113.9:1234")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Mail("bob@external.example", smtp.MailParams{}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Rcpt("alice@test", smtp.RcptParams{}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Data(strings.NewReader("From: bob@external.example\r\nSubject: x\r\n\r\nbody")); err != nil {
		t.Fatal(err)
	}

	var reasons any
	found := false
	for _, e := range sink.events {
		if e.Name == "spam.scored" {
			reasons, found = e.Fields["reasons"], true
		}
	}
	if !found {
		t.Fatal("no spam.scored event was logged")
	}
	if reasons != "SPF fail; Bayesian: likely spam" {
		t.Errorf("logged reasons = %q, want the joined verdict reasons", reasons)
	}
}

// TestInboundSpamRecordedToHistory proves a scored inbound message's verdict is
// recorded with the fields the admin Spam History view shows.
func TestInboundSpamRecordedToHistory(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	hist := &recordingHistory{}
	rec := &recordingScorer{verdict: antispam.Verdict{Score: 9, Spam: true, Reasons: []string{"SPF fail", "listed on DNSBL zen.example"}}}
	b := &Backend{Accounts: accounts, Scorer: rec, History: hist}
	deliverBody(t, b, "203.0.113.9:1234", "bob@external.example", "alice@test",
		"From: bob@external.example\r\nSubject: x\r\n\r\nbody")

	if len(hist.records) != 1 {
		t.Fatalf("history records = %d, want 1", len(hist.records))
	}
	r0 := hist.records[0]
	wantEq(t, "the recorded sender", r0.MailFrom, "bob@external.example")
	wantEq(t, "the recorded spam flag", r0.Spam, true)
	wantEq(t, "the recorded score", r0.Score, 9)
	wantEq(t, "the recorded reasons", r0.Reasons, "SPF fail; listed on DNSBL zen.example")
	wantEq(t, "the recorded client address", r0.RemoteAddr, "203.0.113.9")
}

// TestSpamHistoryFailOpen proves a history write error never fails the delivery:
// the message is still delivered.
func TestSpamHistoryFailOpen(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	b := &Backend{Accounts: accounts, Scorer: &recordingScorer{verdict: antispam.Verdict{Score: 1, Spam: false}}, History: errHistory{}}

	sess, err := b.NewSession("203.0.113.9:1234")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Mail("bob@external.example", smtp.MailParams{}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Rcpt("alice@test", smtp.RcptParams{}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Data(strings.NewReader("From: bob@external.example\r\nSubject: x\r\n\r\nbody")); err != nil {
		t.Fatalf("delivery failed when history recording erred (must be fail-open): %v", err)
	}

	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	inbox, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Errorf("inbox has %d messages, want the message delivered despite the history error", len(inbox))
	}
}

// TestSpamFiledToJunk proves a message the scorer flags as spam is filed to the
// Junk folder (not the inbox) and carries the X-Spam tag through the store.
func TestSpamFiledToJunk(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	b := &Backend{Accounts: accounts, Scorer: &recordingScorer{verdict: antispam.Verdict{Score: 99, Spam: true}}}
	deliverBody(t, b, "203.0.113.9:1234", "bob@external.example", "alice@test",
		"From: bob@external.example\r\nSubject: spam\r\n\r\nbuy now")

	junk := folderMessages(t, mbox, int64(mapi.PrivateFIDJunk))
	wantEq(t, "messages in Junk", len(junk), 1)
	wantEq(t, "messages in the inbox", len(folderMessages(t, mbox, int64(mapi.PrivateFIDInbox))), 0)

	st, err := objectstore.Open(mbox)
	mustNoErr(t, "open the mailbox", err)
	defer st.Close()
	raw, err := st.GetMessageRaw(int64(mapi.PrivateFIDJunk), junk[0].UID)
	mustNoErr(t, "read the filed spam", err)
	wantContains(t, "the filed spam (its X-Spam tag must survive the store round-trip)",
		string(raw), "X-Spam-Flag: YES")
}
