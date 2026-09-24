package mta

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/antispam"
	"hermex/internal/directory"
	"hermex/internal/relay"
)

// recordingDMARC captures the observations the MTA records.
type recordingDMARC struct {
	got []relay.DMARCObservation
	err error
}

func (r *recordingDMARC) RecordDMARC(_ time.Time, o relay.DMARCObservation) error {
	r.got = append(r.got, o)
	return r.err
}

// deliverScored delivers one inbound message from bob@external.example to a local
// mailbox under the given verdict, with dmarcRec as the DMARC recorder.
func deliverScored(t *testing.T, v antispam.Verdict, dmarcRec *recordingDMARC) {
	t.Helper()
	mbox := filepath.Join(t.TempDir(), "alice")
	b := &Backend{
		Accounts: directory.StaticAccounts{"alice@test": {MailboxPath: mbox}},
		Scorer:   &recordingScorer{verdict: v},
		DMARC:    dmarcRec,
	}
	deliverBody(t, b, "203.0.113.9:1234", "bob@bounce.external.example", "alice@test",
		"From: Bob <bob@external.example>\r\nSubject: x\r\n\r\nbody")
}

// TestDMARCRecordedOnlyWhenReportsAreAsked proves a message is counted only for a
// From domain whose record asks for aggregate reports, and that the row carries
// the identifiers, the two alignments and the raw results of the verdict.
func TestDMARCRecordedOnlyWhenReportsAreAsked(t *testing.T) {
	rec := &recordingDMARC{}
	deliverScored(t, antispam.Verdict{DMARC: antispam.AuthPass, SPF: antispam.AuthPass}, rec)
	wantEq(t, "observations without rua", len(rec.got), 0)

	rec = &recordingDMARC{}
	deliverScored(t, antispam.Verdict{
		DMARC: antispam.AuthPass, DMARCReports: true, DMARCDKIMAligned: true, SPF: antispam.AuthError,
		DKIMResults: []antispam.DKIMResult{{Domain: "External.Example", Valid: true}, {Domain: "forged.test", Valid: false}},
	}, rec)
	if len(rec.got) != 1 {
		t.Fatalf("observations = %d, want 1", len(rec.got))
	}
	o := rec.got[0]
	wantEq(t, "policy domain", o.PolicyDomain, "external.example")
	wantEq(t, "source", o.SourceIP, "203.0.113.9")
	wantEq(t, "envelope domain", o.EnvelopeFrom, "bounce.external.example")
	wantEq(t, "DKIM verdict", o.DKIM, "pass")
	wantEq(t, "SPF verdict", o.SPF, "fail")
	wantEq(t, "disposition", o.Disposition, "none")
	wantEq(t, "DKIM results", len(o.DKIMResults), 2)
	wantEq(t, "first DKIM result", o.DKIMResults[0].Domain+"="+o.DKIMResults[0].Result, "external.example=pass")
	wantEq(t, "second DKIM result", o.DKIMResults[1].Result, "fail")
	wantEq(t, "SPF result", o.SPFResults[0].Domain+"="+o.SPFResults[0].Result, "bounce.external.example=temperror")
}

// TestDMARCDispositionFollowsJunkFiling proves a DMARC failure under an enforcing
// policy is reported as quarantined only when it filed the message to Junk.
func TestDMARCDispositionFollowsJunkFiling(t *testing.T) {
	for _, tc := range []struct {
		spam bool
		want string
	}{{true, "quarantine"}, {false, "none"}} {
		rec := &recordingDMARC{}
		deliverScored(t, antispam.Verdict{DMARC: antispam.AuthFail, DMARCReject: true, DMARCReports: true, Spam: tc.spam}, rec)
		if len(rec.got) != 1 {
			t.Fatalf("observations = %d, want 1", len(rec.got))
		}
		wantEq(t, "disposition", rec.got[0].Disposition, tc.want)
	}
}

// TestDMARCRecordFailOpen proves a failed count never fails the delivery.
func TestDMARCRecordFailOpen(t *testing.T) {
	rec := &recordingDMARC{err: errors.New("spool full")}
	deliverScored(t, antispam.Verdict{DMARC: antispam.AuthPass, DMARCReports: true}, rec)
	wantEq(t, "observations attempted", len(rec.got), 1)
}
