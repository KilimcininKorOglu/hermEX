package ews

import (
	"strings"
	"testing"
)

// imipInvitation is a delivered iMIP invitation: an ordinary mail carrying the
// iCalendar REQUEST as a text/calendar part, which is how an invitation from another
// server arrives.
const imipInvitation = "From: Organizer <organizer@hermex.test>\r\n" +
	"To: Alice <alice@hermex.test>\r\n" +
	"Subject: Quarterly Review\r\n" +
	"Date: Wed, 12 Jun 2024 13:46:40 +0000\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/alternative; boundary=IM\r\n\r\n" +
	"--IM\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
	"Please join the quarterly review.\r\n" +
	"--IM\r\n" +
	"Content-Type: text/calendar; method=REQUEST; charset=utf-8\r\n\r\n" +
	meetingRequestICS +
	"--IM--\r\n"

// TestGetItemServesAnInvitationAsMeetingRequest is the load-bearing case: a delivered
// invitation must reach the client as <t:MeetingRequest> carrying MeetingRequestType,
// because that is what the client keys on to offer Accept / Tentative / Decline. A
// plain <t:Message> leaves the invitation indistinguishable from ordinary mail.
func TestGetItemServesAnInvitationAsMeetingRequest(t *testing.T) {
	dir, itemID := seedMeetingRequest(t)
	ts := meetingServer(t, dir)

	_, out := soapPost(t, ts, getItemReq(itemID), true)
	for _, want := range []string{
		`<MeetingRequest xmlns="` + nsTypes + `">`,
		"<ItemClass>IPM.Schedule.Meeting.Request</ItemClass>",
		"<MeetingRequestType>NewMeetingRequest</MeetingRequestType>",
		"<UID>meeting-42</UID>",
		"<Start>2026-07-01T14:00:00Z</Start>",
		"<End>2026-07-01T15:00:00Z</End>",
		"<IsMeeting>true</IsMeeting>",
		"<Location>Boardroom</Location>",
		"<Organizer><Mailbox><EmailAddress>organizer@hermex.test</EmailAddress></Mailbox></Organizer>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("GetItem response is missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, `<Message xmlns="`+nsTypes+`">`) {
		t.Error("an invitation must not be served as an ordinary <t:Message>")
	}
}

// TestGetItemKeepsOrdinaryMailAMessage is the negative control: nothing but a
// scheduling class takes the new element, and ordinary mail still carries its class.
func TestGetItemKeepsOrdinaryMailAMessage(t *testing.T) {
	ts, _ := seededWithMessage(t, plainMessage)

	_, list := soapPost(t, ts, findItemReq("inbox"), true)
	m := itemIDRE.FindStringSubmatch(list)
	if m == nil {
		t.Fatalf("no item id in FindItem response: %s", list)
	}
	_, out := soapPost(t, ts, getItemReq(m[1]), true)
	if !strings.Contains(out, `<Message xmlns="`+nsTypes+`">`) {
		t.Errorf("ordinary mail must stay a <t:Message>:\n%s", out)
	}
	if strings.Contains(out, "MeetingRequest") {
		t.Errorf("ordinary mail must not carry a meeting element:\n%s", out)
	}
	if !strings.Contains(out, "<ItemClass>IPM.Note</ItemClass>") {
		t.Errorf("ordinary mail must carry its stored class:\n%s", out)
	}
}

// TestFindItemCarriesTheItemClass proves a listing row names the message class, so a
// client can tell an invitation from ordinary mail without opening either.
func TestFindItemCarriesTheItemClass(t *testing.T) {
	ts, _ := seededWithMessage(t, imipInvitation)

	_, out := soapPost(t, ts, findItemReq("inbox"), true)
	if !strings.Contains(out, "<ItemClass>IPM.Schedule.Meeting.Request</ItemClass>") {
		t.Errorf("the listing row must name the invitation's class:\n%s", out)
	}
}

// TestMeetingRequestTypeFollowsTheSequence pins the rule: the first invitation is a
// new request, and a later revision (a higher iCalendar SEQUENCE) is a full update.
func TestMeetingRequestTypeFollowsTheSequence(t *testing.T) {
	if got := meetingRequestType(0); got != "NewMeetingRequest" {
		t.Errorf("SEQUENCE 0 = %q, want NewMeetingRequest", got)
	}
	if got := meetingRequestType(3); got != "FullUpdate" {
		t.Errorf("SEQUENCE 3 = %q, want FullUpdate", got)
	}
}
