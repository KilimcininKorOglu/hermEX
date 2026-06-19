package activesync

import (
	"strconv"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// TestMeetingResponseAccept proves the MeetingResponse command resolves the
// referenced request by its FolderId/RequestId, files the appointment through the
// shared workflow, and answers Status 1 with the new CalendarId.
//
// Note: the meeting request is an Inbox email stamped with the appointment named
// properties that inbound calendar parsing (deferred) will set — without those, a
// real inbound request would carry no start/end yet.
func TestMeetingResponseAccept(t *testing.T) {
	ts, dir := seededServer(t)
	uid := seedMeetingRequest(t, dir)

	inbox := strconv.FormatInt(int64(mapi.PrivateFIDInbox), 10)
	req := wbxml.Elem(wbxml.MRMeetingResponse,
		wbxml.Elem(wbxml.MRRequest,
			wbxml.Str(wbxml.MRUserResponse, "1"), // accept
			wbxml.Str(wbxml.MRFolderID, inbox),
			wbxml.Str(wbxml.MRRequestID, strconv.FormatUint(uint64(uid), 10))))
	_, root := postCommand(t, ts, "MeetingResponse", req)

	result := root.Child(wbxml.MRResult)
	if result == nil {
		t.Fatal("MeetingResponse returned no Result")
	}
	if got := result.ChildText(wbxml.MRStatus); got != "1" {
		t.Errorf("Status = %q, want 1 (success)", got)
	}
	if result.ChildText(wbxml.MRCalendarID) == "" {
		t.Error("an accepted meeting should return a CalendarId")
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if cal, _ := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar)); len(cal) != 1 {
		t.Errorf("calendar = %d items, want 1 (the accepted appointment)", len(cal))
	}
}

// seedMeetingRequest puts a meeting-request email in the Inbox (giving it an IMAP
// UID, as inbound mail has) and stamps the appointment named properties that
// inbound calendar parsing would set, returning its UID.
func seedMeetingRequest(t *testing.T, dir string) uint32 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw := "From: organizer@hermex.test\r\nTo: alice@hermex.test\r\nSubject: Review\r\n\r\nmeeting\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole,
		mapi.NameAppointmentEndWhole,
		mapi.NameICalUID,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 1, 14, 0, 0, 0, time.UTC)
	if err := st.SetMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.MakeTag(ids[0], mapi.PtSysTime), Value: mapi.UnixToNTTime(start)},
		{Tag: mapi.MakeTag(ids[1], mapi.PtSysTime), Value: mapi.UnixToNTTime(start.Add(time.Hour))},
		{Tag: mapi.MakeTag(ids[2], mapi.PtUnicode), Value: "eas-meeting-1"},
		{Tag: mapi.PrMessageClass, Value: "IPM.Schedule.Meeting.Request"},
		{Tag: mapi.PrResponseRequested, Value: false}, // no organizer notify in this test
	}); err != nil {
		t.Fatal(err)
	}
	return info.UID
}
