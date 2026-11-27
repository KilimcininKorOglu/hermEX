package webmail2api

import (
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/meeting"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
)

// discardSink drops the events the delivery hooks report.
type discardSink struct{}

func (discardSink) Write(logging.Event) {}

// deliverToAlice delivers an iTIP message from bob into alice's mailbox with the
// delivery hooks installed, the way any daemon delivers one.
func deliverToAlice(t *testing.T, alice, bob, method, uid, vevent string) {
	t.Helper()
	deliverITIP(t, directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: alice},
		"bob@hermex.test":   {MailboxPath: bob},
	}, "bob@hermex.test", method, uid, vevent)
}

// deliverITIP delivers an iTIP message naming bob as organizer from sender into
// alice's mailbox, so a test can have someone other than bob send it.
func deliverITIP(t *testing.T, accounts directory.StaticAccounts, sender, method, uid, vevent string) {
	t.Helper()
	raw := "From: " + sender + "\r\nTo: alice@hermex.test\r\nSubject: Review\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/calendar; method=" + method + "; charset=UTF-8\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//test//EN\r\nMETHOD:" + method + "\r\n" +
		"BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20260901T090000Z\r\nSUMMARY:Review\r\n" +
		"ORGANIZER:mailto:bob@hermex.test\r\nATTENDEE:mailto:alice@hermex.test\r\n" + vevent +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	unresolved, err := mta.DeliverAndRelay(accounts, nil, sender, []string{"alice@hermex.test"}, []byte(raw), time.Now())
	mustNoErr(t, "deliver the "+method, err)
	wantEq(t, "undelivered recipients of the "+method, len(unresolved), 0)
}

// TestACancelFromSomeoneElseCancelsNothing: only the organizer cancels a meeting
// (RFC 5546 section 3.2.5). A CANCEL another account sends, naming bob as
// organizer and the meeting's UID, must leave alice's meeting standing.
func TestACancelFromSomeoneElseCancelsNothing(t *testing.T) {
	env := newMeetingEnv(t)
	st := openMailbox(t, env.alice)
	mustNoErr(t, "auto-accept", st.SetMeetingConfig(objectstore.MeetingConfig{AutoAccept: true}))
	installHooks(t)

	deliverToAlice(t, env.alice, env.bob, "REQUEST", "single@test", "DTSTART:20260910T090000Z\r\nDTEND:20260910T100000Z\r\nSEQUENCE:0\r\n")
	deliverITIP(t, directory.StaticAccounts{
		"alice@hermex.test":   {MailboxPath: env.alice},
		"bob@hermex.test":     {MailboxPath: env.bob},
		"mallory@hermex.test": {MailboxPath: freshMailbox(t)},
	}, "mallory@hermex.test", "CANCEL", "single@test", "DTSTART:20260910T090000Z\r\nDTEND:20260910T100000Z\r\nSEQUENCE:1\r\n")

	rows := listWindow(t, env.do, "2026-09-10T00:00:00Z", "2026-09-11T00:00:00Z")
	if len(rows) != 1 {
		t.Fatalf("listed %d rows, want the one meeting", len(rows))
	}
	wantEq(t, "cancelled by a non-organizer", rows[0].Canceled, false)
}

// installHooks installs the delivery hooks for the test and restores them after.
func installHooks(t *testing.T) {
	t.Helper()
	request, reply, cancel := mta.OnMeetingRequest, mta.OnMeetingReply, mta.OnMeetingCancel
	t.Cleanup(func() { mta.OnMeetingRequest, mta.OnMeetingReply, mta.OnMeetingCancel = request, reply, cancel })
	meeting.InstallDeliveryHooks(logging.New(discardSink{}))
}

// TestCancelledMeetingsAreListedAsCancelled has bob cancel one meeting outright
// and one instance of a series alice accepted. Both stay in alice's listing,
// marked cancelled; the series' other instances are not.
func TestCancelledMeetingsAreListedAsCancelled(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	st := openMailbox(t, alice)
	mustNoErr(t, "auto-accept", st.SetMeetingConfig(objectstore.MeetingConfig{AutoAccept: true}))
	installHooks(t)

	single := "DTSTART:20260910T090000Z\r\nDTEND:20260910T100000Z\r\nSEQUENCE:0\r\n"
	deliverToAlice(t, alice, bob, "REQUEST", "single@test", single)
	deliverToAlice(t, alice, bob, "CANCEL", "single@test", "DTSTART:20260910T090000Z\r\nDTEND:20260910T100000Z\r\nSEQUENCE:1\r\n")
	series := "DTSTART:20260907T060000Z\r\nDTEND:20260907T063000Z\r\nRRULE:FREQ=DAILY;COUNT=3\r\n"
	deliverToAlice(t, alice, bob, "REQUEST", "series@test", series)
	deliverToAlice(t, alice, bob, "CANCEL", "series@test",
		"RECURRENCE-ID:20260908T060000Z\r\nDTSTART:20260908T060000Z\r\nDTEND:20260908T063000Z\r\nSEQUENCE:1\r\n")

	cancelled := map[string]bool{}
	for _, e := range listWindow(t, do, "2026-09-07T00:00:00Z", "2026-09-11T00:00:00Z") {
		cancelled[e.Start] = e.Canceled
	}
	want := map[string]bool{
		"2026-09-07T06:00:00Z": false, "2026-09-08T06:00:00Z": true, "2026-09-09T06:00:00Z": false,
		"2026-09-10T09:00:00Z": true,
	}
	wantEq(t, "listed rows", len(cancelled), len(want))
	for start, c := range want {
		wantEq(t, "cancelled at "+start, cancelled[start], c)
	}
}
