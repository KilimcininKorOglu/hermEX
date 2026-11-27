package webmail2api

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/meeting"
)

// fileInAliceInbox delivers raw mail into alice's Inbox and returns its id.
func fileInAliceInbox(t *testing.T, alice string, raw []byte) filedMail {
	t.Helper()
	st := openMailbox(t, alice)
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0)
	mustNoErr(t, "deliver", err)
	return filedMail{id: "inbox:" + strconv.FormatUint(uint64(info.UID), 10), msgID: info.ID}
}

// filedMail is a delivered message: its webmail id and its store message id.
type filedMail struct {
	id    string
	msgID int64
}

// inviteView reads what the reader shows about the message id.
func inviteView(t *testing.T, do requestFunc, id string) inviteJSON {
	t.Helper()
	return okBody[inviteJSON](t, "invite", do(http.MethodGet, "/api/v1/mail/invite?id="+id, ""))
}

// counterMail is bob's proposal of a new time for alice's meeting uid.
func counterMail(uid string) []byte {
	return []byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: New Time Proposed: Quarterly review\r\n" +
		"Content-Type: text/calendar; method=COUNTER; charset=utf-8\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:COUNTER\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\n" +
		"DTSTART:20260615T130000Z\r\nDTEND:20260615T140000Z\r\nORGANIZER:mailto:alice@hermex.test\r\n" +
		"ATTENDEE;PARTSTAT=TENTATIVE:mailto:bob@hermex.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
}

// cancelMail is bob's cancellation of the meeting uid he invited alice to.
func cancelMail(uid string) []byte {
	return []byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: Canceled: Quarterly review\r\n" +
		"Content-Type: text/calendar; method=CANCEL; charset=utf-8\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:CANCEL\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\n" +
		"DTSTART:20260615T090000Z\r\nDTEND:20260615T100000Z\r\nSEQUENCE:1\r\nSTATUS:CANCELLED\r\n" +
		"ORGANIZER:mailto:bob@hermex.test\r\nATTENDEE:mailto:alice@hermex.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
}

// TestInviteShowsTheRequestAndTheAnswer reads a request before and after alice
// answers it: the reader learns it is a request to answer, and then which answer
// she gave.
func TestInviteShowsTheRequestAndTheAnswer(t *testing.T) {
	env := newMeetingEnv(t)
	do, alice := env.do, env.alice
	id := fileInAliceInbox(t, alice, inviteMail("view-1@hermex.test")).id

	v := inviteView(t, do, id)
	wantEq(t, "kind", v.Kind, "request")
	wantEq(t, "response requested", v.ResponseRequested, true)
	wantEq(t, "answer before answering", v.Response, "")
	wantStatus(t, "accept", do(http.MethodPost, "/api/v1/mail/rsvp", `{"id":"`+id+`","response":"accept"}`), http.StatusOK)
	wantEq(t, "answer after accepting", inviteView(t, do, id).Response, "accept")
}

// TestInviteShowsTheOwnInvitation reads the copy of the invitation alice sent:
// she organizes the meeting, so it offers nothing to answer.
func TestInviteShowsTheOwnInvitation(t *testing.T) {
	env := newMeetingEnv(t)
	do, alice := env.do, env.alice
	createEvent(t, do, `{"summary":"Review","start":"2026-09-08T09:00:00Z","end":"2026-09-08T10:00:00Z",`+
		`"attendees":["bob@hermex.test"],"sendInvite":true}`)
	sent, err := openMailbox(t, alice).ListMessages(int64(mapi.PrivateFIDSentItems))
	mustNoErr(t, "list alice's Sent Items", err)
	if len(sent) != 1 {
		t.Fatalf("alice's Sent Items holds %d messages, want her invitation", len(sent))
	}
	v := inviteView(t, do, "sent:"+strconv.FormatUint(uint64(sent[0].UID), 10))
	wantEq(t, "kind", v.Kind, "request")
	wantEq(t, "organizer", v.IsOrganizer, true)
}

// TestInviteShowsAnAnswerAndAProposal reads an attendee's answer and a proposal of
// a new time as the organizer receives them.
func TestInviteShowsAnAnswerAndAProposal(t *testing.T) {
	env := newMeetingEnv(t)
	do, alice := env.do, env.alice
	replyID := fileInAliceInbox(t, alice, replyMail("view-2@hermex.test")).id
	counterID := fileInAliceInbox(t, alice, counterMail("view-2@hermex.test")).id

	reply := inviteView(t, do, replyID)
	wantEq(t, "reply kind", reply.Kind, "response")
	wantEq(t, "reply answer", reply.Response, "accept")
	counter := inviteView(t, do, counterID)
	wantEq(t, "counter kind", counter.Kind, "counter")
	wantEq(t, "proposed start", counter.ProposedStart, "2026-06-15T13:00:00Z")
	wantEq(t, "proposed end", counter.ProposedEnd, "2026-06-15T14:00:00Z")
}

// TestInviteRemovesACanceledMeeting follows a meeting alice accepted and bob then
// canceled: the cancellation offers the meeting for removal, removing takes it off
// her calendar, and then nothing is left to remove.
func TestInviteRemovesACanceledMeeting(t *testing.T) {
	env := newMeetingEnv(t)
	do, alice := env.do, env.alice
	requestID := fileInAliceInbox(t, alice, inviteMail("view-3@hermex.test")).id
	wantStatus(t, "accept", do(http.MethodPost, "/api/v1/mail/rsvp", `{"id":"`+requestID+`","response":"accept"}`), http.StatusOK)
	cancel := fileInAliceInbox(t, alice, cancelMail("view-3@hermex.test"))
	cancelID := cancel.id
	// The delivery pass applies a cancellation from the meeting's organizer.
	if _, err := meeting.ProcessCancellation(openMailbox(t, alice), "bob@hermex.test", cancel.msgID); err != nil {
		t.Fatal(err)
	}

	v := inviteView(t, do, cancelID)
	wantEq(t, "kind", v.Kind, "cancellation")
	wantEq(t, "removable", v.Removable, true)
	remove := `{"id":"` + cancelID + `"}`
	wantStatus(t, "remove", do(http.MethodPost, "/api/v1/mail/remove-from-calendar", remove), http.StatusOK)
	wantEq(t, "alice's calendar", calendarCount(t, alice), 0)
	wantEq(t, "removable after removing", inviteView(t, do, cancelID).Removable, false)
	wantStatus(t, "remove again", do(http.MethodPost, "/api/v1/mail/remove-from-calendar", remove), http.StatusConflict)
}

// TestInviteRefusesWhatIsNoInvite: an id naming nothing, a malformed id and a
// plain mail offer no invite to act on, and removing through them leaves the
// accepted meeting on the calendar.
func TestInviteRefusesWhatIsNoInvite(t *testing.T) {
	env := newMeetingEnv(t)
	do, alice := env.do, env.alice
	requestID := fileInAliceInbox(t, alice, inviteMail("view-5@hermex.test")).id
	wantStatus(t, "accept", do(http.MethodPost, "/api/v1/mail/rsvp", `{"id":"`+requestID+`","response":"accept"}`), http.StatusOK)
	plainID := fileInAliceInbox(t, alice, []byte("From: bob@hermex.test\r\nSubject: lunch\r\n\r\nhi\r\n")).id
	for _, id := range []string{"inbox:999999", "not-an-id", plainID} {
		if rec := do(http.MethodPost, "/api/v1/mail/remove-from-calendar", `{"id":"`+id+`"}`); rec.Code == http.StatusOK {
			t.Errorf("remove through %s answered 200", id)
		}
	}
	for _, id := range []string{"inbox:999999", "not-an-id"} {
		if rec := do(http.MethodGet, "/api/v1/mail/invite?id="+id, ""); rec.Code == http.StatusOK {
			t.Errorf("invite view of %s answered 200", id)
		}
	}
	wantEq(t, "alice's calendar", calendarCount(t, alice), 1)
}

// TestInviteKeepsAMeetingNobodyCanceled refuses to remove through a cancellation
// the meeting's organizer never sent: the meeting stays.
func TestInviteKeepsAMeetingNobodyCanceled(t *testing.T) {
	env := newMeetingEnv(t)
	do, alice := env.do, env.alice
	requestID := fileInAliceInbox(t, alice, inviteMail("view-4@hermex.test")).id
	wantStatus(t, "accept", do(http.MethodPost, "/api/v1/mail/rsvp", `{"id":"`+requestID+`","response":"accept"}`), http.StatusOK)
	cancelID := fileInAliceInbox(t, alice, cancelMail("view-4@hermex.test")).id

	wantEq(t, "removable", inviteView(t, do, cancelID).Removable, false)
	wantStatus(t, "remove", do(http.MethodPost, "/api/v1/mail/remove-from-calendar", `{"id":"`+cancelID+`"}`), http.StatusConflict)
	wantEq(t, "alice's calendar", calendarCount(t, alice), 1)
}
