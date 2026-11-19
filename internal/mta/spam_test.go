package mta

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// forgedResults is an inbound message carrying an Authentication-Results field a
// sender wrote under this server's name, claiming a DKIM pass.
const forgedResults = "Authentication-Results: mx.hermex.test; dkim=pass header.d=bank.example\r\n" +
	"From: ceo@bank.example\r\nSubject: wire money\r\n\r\nbody\r\n"

// ownResults counts the Authentication-Results fields under our authserv-id in
// the header block of a filed copy, and reports whether the forged pass is there.
func ownResults(raw []byte) (n int, forged bool) {
	head, _, _ := strings.Cut(string(raw), "\r\n\r\n")
	for line := range strings.SplitSeq(head, "\r\n") {
		if strings.HasPrefix(line, "Authentication-Results: mx.hermex.test") {
			n++
		}
	}
	return n, strings.Contains(head, "header.d=bank.example")
}

// TestInboundAuthResultsReplaceTheSendersClaim files inbound mail through a
// session whose authserv-id is mx.hermex.test. The forged field must be gone
// from the filed copy whether or not the message was scored (RFC 8601
// section 5), a scored copy carries exactly one field, this server's, and an
// authenticated submission is left as its sender wrote it.
func TestInboundAuthResultsReplaceTheSendersClaim(t *testing.T) {
	newSession := func(scorer SpamScorer, authUser string) *session {
		return &session{authservID: "mx.hermex.test", scorer: scorer, authUser: authUser,
			targets: []target{{addr: "alice@test", path: "/mb/alice"}}, from: "x@bank.example"}
	}
	scored := newSession(&recordingScorer{verdict: antispam.Verdict{SPF: antispam.AuthFail, DKIM: antispam.AuthFail, DMARC: antispam.AuthFail}}, "")
	sc := scored.scoreInbound([]byte(forgedResults), time.Now())
	if n, forged := ownResults(sc.raw); n != 1 || forged {
		t.Errorf("scored copy: %d fields under our id, forged pass present %v; want 1 and false\n%s", n, forged, sc.raw)
	}
	if !strings.Contains(string(sc.raw), "dkim=fail") {
		t.Errorf("the scored copy does not carry this server's dkim=fail\n%s", sc.raw)
	}
	unscored := newSession(nil, "").scoreInbound([]byte(forgedResults), time.Now())
	if n, _ := ownResults(unscored.raw); n != 0 {
		t.Errorf("an unscored copy kept %d fields under our id", n)
	}
	submitted := newSession(nil, "alice@test").scoreInbound([]byte(forgedResults), time.Now())
	if string(submitted.raw) != forgedResults {
		t.Error("an authenticated submission was rewritten")
	}
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

// TestFiledSpamCarriesOnlyTheVerdictFlag proves a sender's forged "X-Spam-Flag: NO"
// does not reach the reader beside the verdict, both from the cached copy and from
// the copy the store regenerates through the export. A client that reads the last
// occurrence of the field would otherwise trust the sender's own answer.
func TestFiledSpamCarriesOnlyTheVerdictFlag(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	b := &Backend{Accounts: accounts, Scorer: &recordingScorer{verdict: antispam.Verdict{Score: 99, Spam: true}}}
	deliverBody(t, b, "203.0.113.9:1234", "bob@external.example", "alice@test",
		"From: bob@external.example\r\nX-Spam-Flag: NO\r\nSubject: spam\r\n\r\nbuy now")

	junk := folderMessages(t, mbox, int64(mapi.PrivateFIDJunk))
	wantEq(t, "messages in Junk", len(junk), 1)
	for _, how := range []string{"cached", "regenerated"} {
		if how == "regenerated" {
			mustNoErr(t, "drop the cached copy", os.RemoveAll(filepath.Join(mbox, "eml")))
		}
		st, err := objectstore.Open(mbox)
		mustNoErr(t, "open the mailbox", err)
		raw, err := st.GetMessageRaw(int64(mapi.PrivateFIDJunk), junk[0].UID)
		st.Close()
		mustNoErr(t, "read the filed spam", err)
		head, _, _ := strings.Cut(string(raw), "\r\n\r\n")
		if n := strings.Count(strings.ToLower(head), "x-spam-flag:"); n != 1 || !strings.Contains(head, "X-Spam-Flag: YES") {
			t.Errorf("%s copy carries %d X-Spam-Flag fields, want only the verdict:\n%s", how, n, head)
		}
	}
}
