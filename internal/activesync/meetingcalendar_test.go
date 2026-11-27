package activesync

import (
	"bytes"
	"strconv"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/meeting"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/wbxml"
)

// respondInCalendar answers the Calendar item serverID with userResponse under
// 16.1, asking the server to send the response, and returns the Result Status.
func respondInCalendar(t *testing.T, post func(*wbxml.Node) *wbxml.Node, serverID, userResponse string) string {
	t.Helper()
	root := post(wbxml.Elem(wbxml.MRMeetingResponse, wbxml.Elem(wbxml.MRRequest,
		wbxml.Str(wbxml.MRUserResponse, userResponse),
		wbxml.Str(wbxml.MRFolderID, strconv.FormatInt(int64(mapi.PrivateFIDCalendar), 10)),
		wbxml.Str(wbxml.MRRequestID, serverID),
		wbxml.Elem(wbxml.MRSendResponse))))
	return root.Child(wbxml.MRResult).ChildText(wbxml.MRStatus)
}

// TestMeetingResponseFromTheCalendar proves a meeting is answered through its
// Calendar item, the ServerId a calendar Sync gave it ([MS-ASCMD] MeetingResponse:
// the request "in the user's Inbox folder or Calendar folder").
func TestMeetingResponseFromTheCalendar(t *testing.T) {
	ts, attendee, organizer := organizerServer(t)
	uid := seedLocalRequest(t, attendee, planningICS)
	calendarID := acceptRequest(t, ts, "16.1", uid).ChildText(wbxml.MRCalendarID)
	post := func(n *wbxml.Node) *wbxml.Node { return postVersioned(t, ts, "16.1", "MeetingResponse", n) }

	if got := respondInCalendar(t, post, calendarID, "3"); got != "1" {
		t.Fatalf("Status = %q, want 1: the Calendar item was not answered", got)
	}
	st, err := objectstore.Open(attendee)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	// A failed read would return no items and pass as an emptied calendar.
	cal, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	mustNoErr(t, "list the calendar", err)
	if len(cal) != 0 {
		t.Errorf("calendar holds %d items after the decline, want 0", len(cal))
	}
	got := organizerInbox(t, organizer)
	if len(got) != 1 || !bytes.Contains(got[0], []byte("PARTSTAT=DECLINED")) {
		t.Errorf("organizer did not receive the decline: %d messages", len(got))
	}
}

// TestMeetingResponseRefusesTheOrganizersOwnMeeting proves a Calendar item the
// user organized is not a meeting request to answer ([MS-ASCMD] MeetingResponse
// Status 2: "The request points to an appointment in which the user is the
// organizer").
func TestMeetingResponseRefusesTheOrganizersOwnMeeting(t *testing.T) {
	ts, attendee, organizer := organizerServer(t)
	st, err := objectstore.Open(attendee)
	if err != nil {
		t.Fatal(err)
	}
	tags, err := meeting.ResolveTags(st)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"},
		{Tag: mapi.PrSubject, Value: "My own meeting"},
		{Tag: tags.State, Value: int32(0x1)}, // a meeting, organized here
	}})
	mustNoErr(t, "close the store", st.Close())
	if err != nil {
		t.Fatal(err)
	}
	sid := strconv.FormatInt(id, 10)
	before := calendarChangeNumber(t, attendee, sid)
	post := func(n *wbxml.Node) *wbxml.Node { return postVersioned(t, ts, "16.1", "MeetingResponse", n) }
	if got := respondInCalendar(t, post, sid, "1"); got != "2" {
		t.Errorf("Status = %q, want 2 (the user is the organizer)", got)
	}
	wantEq(t, "the organizer's item change number after the refusal", calendarChangeNumber(t, attendee, sid), before)
	if n := len(organizerInbox(t, organizer)); n != 0 {
		t.Errorf("a refused response still sent %d messages", n)
	}
}

// TestMeetingResponseRefusesWhatIsNoMeeting proves a RequestId that names no
// meeting is refused with Status 2 ([MS-ASCMD] MeetingResponse: the meeting
// request is invalid) and sends nothing: a plain appointment with no attendees,
// and ids the server never issued.
func TestMeetingResponseRefusesWhatIsNoMeeting(t *testing.T) {
	ts, attendee, organizer := organizerServer(t)
	st, err := objectstore.Open(attendee)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"},
		{Tag: mapi.PrSubject, Value: "Dentist"},
	}})
	mustNoErr(t, "close the store", st.Close())
	if err != nil {
		t.Fatal(err)
	}
	sid := strconv.FormatInt(id, 10)
	before := calendarChangeNumber(t, attendee, sid)
	post := func(n *wbxml.Node) *wbxml.Node { return postVersioned(t, ts, "16.1", "MeetingResponse", n) }
	for _, rid := range []string{sid, "abc", "-1", "999999999"} {
		if got := respondInCalendar(t, post, rid, "1"); got != "2" {
			t.Errorf("RequestId %q Status = %q, want 2", rid, got)
		}
	}
	wantEq(t, "the appointment's change number after the refusals", calendarChangeNumber(t, attendee, sid), before)
	if n := len(organizerInbox(t, organizer)); n != 0 {
		t.Errorf("a refused response still sent %d messages", n)
	}
}
