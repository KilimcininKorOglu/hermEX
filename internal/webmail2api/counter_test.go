package webmail2api

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// counterInvite files in alice's inbox a request bob organizes.
func counterInvite(t *testing.T, alice string) string {
	t.Helper()
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\nUID:counter-1@test\r\nSUMMARY:Sync\r\n" +
		"DTSTART:20260908T090000Z\r\nDTEND:20260908T100000Z\r\nORGANIZER:mailto:bob@hermex.test\r\n" +
		"ATTENDEE:mailto:alice@hermex.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	raw := "From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: Sync\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/calendar; method=REQUEST; charset=utf-8\r\n\r\n" + ics
	st := openMailbox(t, alice)
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0)
	mustNoErr(t, "file the invite", err)
	return "inbox:" + strconv.FormatUint(uint64(info.UID), 10)
}

// TestCounterProposalReachesTheOrganizer proposes a new time for bob's meeting:
// exactly one proposal reaches bob.
func TestCounterProposalReachesTheOrganizer(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := counterInvite(t, alice)
	wantStatus(t, "propose", do(http.MethodPost, "/api/v1/mail/propose-time",
		`{"id":"`+id+`","start":"2026-09-08T11:00:00Z","end":"2026-09-08T12:00:00Z"}`), http.StatusOK)
	lastOf(t, folderMail(t, bob, int64(mapi.PrivateFIDInbox)), 1)
}

// TestCounterProposalWireForm is the message the proposal sends: built through the
// shared mail export, so it carries a Message-ID and the calendar part as the
// COUNTER alternative, and a summary holding a line break reaches neither a header
// line nor a new iCalendar line.
func TestCounterProposalWireForm(t *testing.T) {
	raw, err := buildCounterRequest("alice@hermex.test", "Bob <bob@hermex.test>", eventJSON{UID: "counter-1@test",
		Summary: "Sync\r\nBcc: attacker@evil.example", Start: "2026-09-08T11:00:00Z", End: "2026-09-08T12:00:00Z"})
	mustNoErr(t, "build", err)
	msg := string(raw)
	for _, want := range []string{"method=COUNTER", "METHOD:COUNTER", "UID:counter-1@test", "DTSTART:20260908T110000Z",
		"DTEND:20260908T120000Z", "ORGANIZER:mailto:bob@hermex.test", "DTSTAMP:", "Message-ID:"} {
		wantContains(t, "counter-proposal", msg, want)
	}
	wantNoInjectedLines(t, msg)
	wantContains(t, "escaped summary", msg, `SUMMARY:Sync\nBcc: attacker@evil.example`)
}

// TestCounterProposalRefusesAnUnreadableTime proposes a start that is not a time.
// The value used to be written onto the DTSTART line verbatim, so a line break in
// it added iCalendar properties of the client's choosing.
func TestCounterProposalRefusesAnUnreadableTime(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := counterInvite(t, alice)
	wantStatus(t, "propose", do(http.MethodPost, "/api/v1/mail/propose-time",
		`{"id":"`+id+`","start":"2026-09-08T11:00:00Z\r\nATTENDEE:mailto:carol@hermex.test"}`), http.StatusBadRequest)
	wantEq(t, "bob's inbox", len(folderMail(t, bob, int64(mapi.PrivateFIDInbox))), 0)
}
