package activesync

import (
	"bytes"
	"strconv"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

const dailyICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\n" +
	"BEGIN:VEVENT\r\nUID:eas-daily-1\r\nDTSTAMP:20260601T080000Z\r\nSUMMARY:Standup\r\n" +
	"DTSTART:20260706T090000Z\r\nDTEND:20260706T091500Z\r\nRRULE:FREQ=DAILY;COUNT=5\r\n" +
	"ORGANIZER:mailto:" + localOrganizer + "\r\nATTENDEE;RSVP=TRUE:mailto:" + testUser + "\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

// answerInstance answers one occurrence of the Calendar item serverID under 16.1,
// asking the server to send the response, and returns the Result.
func answerInstance(t *testing.T, post func(*wbxml.Node) *wbxml.Node, serverID, userResponse, instance string) *wbxml.Node {
	t.Helper()
	root := post(wbxml.Elem(wbxml.MRMeetingResponse, wbxml.Elem(wbxml.MRRequest,
		wbxml.Str(wbxml.MRUserResponse, userResponse),
		wbxml.Str(wbxml.MRFolderID, strconv.FormatInt(int64(mapi.PrivateFIDCalendar), 10)),
		wbxml.Str(wbxml.MRRequestID, serverID),
		wbxml.Str(wbxml.MRInstanceID, instance),
		wbxml.Elem(wbxml.MRSendResponse))))
	return root.Child(wbxml.MRResult)
}

// TestMeetingResponseAnswersOneInstance proves an InstanceId answers that one
// occurrence ([MS-ASCMD] InstanceId): declining it takes it out of the attendee's
// series, the rest of the series stays, and the organizer is told about that
// occurrence alone, by its RECURRENCE-ID.
func TestMeetingResponseAnswersOneInstance(t *testing.T) {
	ts, attendee, organizer := organizerServer(t)
	uid := seedLocalRequest(t, attendee, dailyICS)
	calendarID := acceptRequest(t, ts, "16.1", uid).ChildText(wbxml.MRCalendarID)
	post := func(n *wbxml.Node) *wbxml.Node { return postVersioned(t, ts, "16.1", "MeetingResponse", n) }
	before := calendarChangeNumber(t, attendee, calendarID)

	result := answerInstance(t, post, calendarID, "3", "2026-07-08T09:00:00.000Z")
	if got := result.ChildText(wbxml.MRStatus); got != "1" {
		t.Fatalf("Status = %q, want 1", got)
	}
	// The write moves the change number, the signal the refusal test relies on.
	if calendarChangeNumber(t, attendee, calendarID) == before {
		t.Error("declining an instance left the series' change number where it was")
	}
	if got := result.ChildText(wbxml.MRInstanceID); got != "2026-07-08T09:00:00.000Z" {
		t.Errorf("Result InstanceId = %q, want the request's", got)
	}
	if body := onlyCalendarICal(t, attendee); !bytes.Contains(body, []byte("RRULE:FREQ=DAILY;COUNT=5")) ||
		!bytes.Contains(body, []byte("EXDATE:20260708T090000Z")) {
		t.Errorf("the series does not exclude the declined occurrence:\n%s", body)
	}
	got := organizerInbox(t, organizer)
	if len(got) != 1 {
		t.Fatalf("organizer received %d responses, want 1", len(got))
	}
	for _, want := range []string{"RECURRENCE-ID:20260708T090000Z", "PARTSTAT=DECLINED", "DTSTART:20260708T090000Z"} {
		if !bytes.Contains(got[0], []byte(want)) {
			t.Errorf("the response lacks %q:\n%s", want, got[0])
		}
	}
}

// onlyCalendarICal returns the iCalendar of the one item in the mailbox's Calendar.
func onlyCalendarICal(t *testing.T, dir string) []byte {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cal, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	mustNoErr(t, "list the calendar", err)
	if len(cal) != 1 {
		t.Fatalf("calendar holds %d items, want the one series", len(cal))
	}
	pv, err := st.GetMessageProperties(cal[0].ID, mapi.PrIcalOriginal)
	if err != nil {
		t.Fatal(err)
	}
	// An empty body would pass every "has no EXDATE" check, so a missing
	// iCalendar is a failure, not an unchanged series.
	stored, _ := pv.Get(mapi.PrIcalOriginal)
	body, _ := stored.([]byte)
	if !bytes.Contains(body, []byte("BEGIN:VEVENT")) {
		t.Fatalf("the series carries no iCalendar: %q", body)
	}
	return body
}

// calendarChangeNumber returns the change number of the Calendar item serverID.
func calendarChangeNumber(t *testing.T, dir, serverID string) uint64 {
	t.Helper()
	id, err := strconv.ParseInt(serverID, 10, 64)
	mustNoErr(t, "parse the calendar item id", err)
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	cn, err := st.MessageChangeNumber(id)
	mustNoErr(t, "read the change number", err)
	return cn
}

// TestMeetingResponseInstanceStatuses proves the InstanceId failures ([MS-ASCMD]
// InstanceId): a value that is not a date-time is 104, an instance the series does
// not have is 2, and an instance of a meeting that does not recur is 146. A refused
// instance leaves the attendee's series as it was. The daily series has five
// occurrences from 2026-07-06 (RRULE COUNT=5), so the day before the first and the
// day after the last are instances it does not have.
func TestMeetingResponseInstanceStatuses(t *testing.T) {
	for _, tc := range []struct {
		name, ics, instance, want string
	}{
		{"bad format", dailyICS, "8 July", "104"},
		{"no such instance", dailyICS, "2026-07-08T10:00:00.000Z", "2"},
		{"before the series", dailyICS, "2026-07-05T09:00:00.000Z", "2"},
		{"after the last occurrence", dailyICS, "2026-07-11T09:00:00.000Z", "2"},
		{"not recurring", planningICS, "2026-07-02T14:00:00.000Z", "146"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, attendee, organizer := organizerServer(t)
			uid := seedLocalRequest(t, attendee, tc.ics)
			calendarID := acceptRequest(t, ts, "16.1", uid).ChildText(wbxml.MRCalendarID)
			before := calendarChangeNumber(t, attendee, calendarID)
			post := func(n *wbxml.Node) *wbxml.Node { return postVersioned(t, ts, "16.1", "MeetingResponse", n) }
			if got := answerInstance(t, post, calendarID, "3", tc.instance).ChildText(wbxml.MRStatus); got != tc.want {
				t.Errorf("Status = %q, want %s", got, tc.want)
			}
			if n := len(organizerInbox(t, organizer)); n != 0 {
				t.Errorf("a refused response still sent %d messages", n)
			}
			// Every write to a message allocates a new change number, so an
			// unchanged one proves the refused response wrote nothing, whether the
			// item keeps an iCalendar (a series) or only properties (one meeting).
			wantEq(t, "the item's change number after a refused response",
				calendarChangeNumber(t, attendee, calendarID), before)
		})
	}
}
