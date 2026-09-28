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

// TestProposalReachesTheOrganizer answers tentatively with a new time: exactly one
// answer reaches bob, filed as the counter proposal his client reads the proposed
// time from ([MS-OXOCAL] 3.1.4.8.4.1), and it carries the note alice wrote.
func TestProposalReachesTheOrganizer(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := counterInvite(t, alice)
	wantStatus(t, "propose", do(http.MethodPost, "/api/v1/mail/rsvp", `{"id":"`+id+`","response":"tentative",`+
		`"proposeStart":"2026-09-08T11:00:00Z","proposeEnd":"2026-09-08T12:00:00Z","comment":"Mornings are full."}`), http.StatusOK)
	wire := lastOf(t, folderMail(t, bob, int64(mapi.PrivateFIDInbox)), 1)
	for _, want := range []string{"METHOD:COUNTER", "DTSTART:20260908T110000Z", "DTEND:20260908T120000Z",
		"Subject: New Time Proposed: Sync", "Mornings are full.", "Message-ID:"} {
		wantContains(t, "the proposal", wire, want)
	}

	st := openMailbox(t, bob)
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list bob's inbox", err)
	msg, err := st.OpenMessage(msgs[0].ID)
	mustNoErr(t, "open the proposal", err)
	wantEq(t, "class", propStr(msg.Props, mapi.PrMessageClass), "IPM.Schedule.Meeting.Resp.Tent")
	v, _ := msg.Props.Get(namedTag(t, st, mapi.NameAppointmentCounterProposal, mapi.PtBoolean))
	wantEq(t, "counter proposal", v, any(true))
	start, _ := msg.Props.Get(namedTag(t, st, mapi.NameAppointmentProposedStartWhole, mapi.PtSysTime))
	wantEq(t, "proposed start", start, any(mapi.UnixToNTTime(time.Date(2026, 9, 8, 11, 0, 0, 0, time.UTC))))
}

// TestDeclineWithAProposalTakesTheMeetingOff declines with a new time, Outlook's
// "Decline and propose new time": the organizer is offered the time, and the
// meeting leaves alice's calendar as any decline takes it off.
func TestDeclineWithAProposalTakesTheMeetingOff(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := counterInvite(t, alice)
	wantStatus(t, "accept", do(http.MethodPost, "/api/v1/mail/rsvp", `{"id":"`+id+`","response":"accept","send":false}`), http.StatusOK)
	wantStatus(t, "decline and propose", do(http.MethodPost, "/api/v1/mail/rsvp", `{"id":"`+id+`","response":"decline",`+
		`"proposeStart":"2026-09-09T09:00:00Z","proposeEnd":"2026-09-09T10:00:00Z"}`), http.StatusOK)
	wantContains(t, "the proposal", lastOf(t, folderMail(t, bob, int64(mapi.PrivateFIDInbox)), 1), "METHOD:COUNTER")
	wantEq(t, "alice's calendar", calendarCount(t, alice), 0)
}

// TestProposalRefusesAnUnreadableTime proposes a time that is not one. The value
// used to be written onto the DTSTART line verbatim, so a line break in it added
// iCalendar properties of the client's choosing. A half span and a span that ends
// before it starts are refused too, and nothing reaches bob.
func TestProposalRefusesAnUnreadableTime(t *testing.T) {
	for _, span := range []string{
		`"proposeStart":"2026-09-08T11:00:00Z\r\nATTENDEE:mailto:carol@hermex.test","proposeEnd":"2026-09-08T12:00:00Z"`,
		`"proposeStart":"2026-09-08T11:00:00Z"`,
		`"proposeStart":"2026-09-08T12:00:00Z","proposeEnd":"2026-09-08T11:00:00Z"`,
	} {
		do, alice, bob := meetingHarness(t)
		id := counterInvite(t, alice)
		wantStatus(t, span, do(http.MethodPost, "/api/v1/mail/rsvp", `{"id":"`+id+`","response":"tentative",`+span+`}`), http.StatusBadRequest)
		wantEq(t, "bob's inbox", len(folderMail(t, bob, int64(mapi.PrivateFIDInbox))), 0)
		wantEq(t, "alice's calendar", calendarCount(t, alice), 0)
	}
}

// TestProposalMustBeSent refuses a proposal the reader chose not to send: a time
// nobody receives proposes nothing.
func TestProposalMustBeSent(t *testing.T) {
	do, alice, _ := meetingHarness(t)
	id := counterInvite(t, alice)
	wantStatus(t, "unsent proposal", do(http.MethodPost, "/api/v1/mail/rsvp", `{"id":"`+id+`","response":"tentative","send":false,`+
		`"proposeStart":"2026-09-08T11:00:00Z","proposeEnd":"2026-09-08T12:00:00Z"}`), http.StatusBadRequest)
	wantEq(t, "alice's calendar", calendarCount(t, alice), 0)
}

// TestAnswerWithoutSendingTellsNobody answers with Outlook's "Don't send a
// response": the answer is recorded on alice's calendar, and bob receives nothing
// and alice keeps nothing in Sent Items.
func TestAnswerWithoutSendingTellsNobody(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := counterInvite(t, alice)
	wantStatus(t, "accept silently", do(http.MethodPost, "/api/v1/mail/rsvp", `{"id":"`+id+`","response":"accept","send":false}`), http.StatusOK)
	wantEq(t, "alice's calendar", calendarCount(t, alice), 1)
	wantEq(t, "bob's inbox", len(folderMail(t, bob, int64(mapi.PrivateFIDInbox))), 0)
	wantEq(t, "alice's Sent Items", sentItems(t, alice), 0)
	wantEq(t, "the answer shown", inviteView(t, do, id).Response, "accept")
}
