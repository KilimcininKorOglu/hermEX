package activesync

import (
	"strconv"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// TestMeetingResponseAccept proves the full inbound path: a delivered meeting-request
// email (its appointment parsed from the text/calendar part on import) is resolved by
// its FolderId/RequestId, accepted through the shared workflow, and answered Status 1
// with the new CalendarId, and the filed appointment carries the invitation's UID.
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
	cal, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	mustNoErr(t, "list the calendar", err)
	if len(cal) != 1 {
		t.Fatalf("calendar = %d items, want 1 (the accepted appointment)", len(cal))
	}
	// The appointment carries the invitation's UID, proving the request's
	// properties came from the parsed text/calendar part, not an empty shell.
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameICalUID})
	if err != nil {
		t.Fatal(err)
	}
	uidTag := mapi.MakeTag(ids[0], mapi.PtUnicode)
	pv, err := st.GetMessageProperties(cal[0].ID, uidTag)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := pv.Get(uidTag); v != "eas-meeting-1" {
		t.Errorf("filed appointment UID = %v, want eas-meeting-1 (from the parsed invite)", v)
	}
}

// TestMeetingResponseRefusesInvalidRequests proves the Requests [MS-ASCMD]
// MeetingResponse refuses with Status 2 ("the meeting request is invalid") file
// nothing: a UserResponse outside 1 (Accept), 2 (Tentative) and 3 (Decline), a
// RequestId naming an ordinary mail, a RequestId the folder does not hold, and a
// FolderId that is not a number.
func TestMeetingResponseRefusesInvalidRequests(t *testing.T) {
	ts, dir := seededServer(t)
	invite := strconv.FormatUint(uint64(seedMeetingRequest(t, dir)), 10)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(convMsgRoot), time.Now(), 0)
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	inbox := strconv.FormatInt(int64(mapi.PrivateFIDInbox), 10)
	for _, tc := range []struct{ name, response, folder, request string }{
		{"response 0", "0", inbox, invite},
		{"response 4", "4", inbox, invite},
		{"response not a number", "yes", inbox, invite},
		{"ordinary mail", "1", inbox, strconv.FormatUint(uint64(plain.UID), 10)},
		{"no such message", "1", inbox, "4000000"},
		{"folder not a number", "1", "inbox", invite},
	} {
		req := wbxml.Elem(wbxml.MRMeetingResponse, wbxml.Elem(wbxml.MRRequest,
			wbxml.Str(wbxml.MRUserResponse, tc.response),
			wbxml.Str(wbxml.MRFolderID, tc.folder),
			wbxml.Str(wbxml.MRRequestID, tc.request)))
		_, root := postCommand(t, ts, "MeetingResponse", req)
		if got := root.Child(wbxml.MRResult).ChildText(wbxml.MRStatus); got != "2" {
			t.Errorf("%s: Status = %q, want 2", tc.name, got)
		}
	}
	st, err = objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// A failed read would return no items and pass as "nothing filed".
	cal, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	mustNoErr(t, "list the calendar", err)
	if len(cal) != 0 {
		t.Errorf("refused responses filed %d calendar items, want 0", len(cal))
	}
}

// seedMeetingRequest delivers a meeting-request email (a text/calendar METHOD:REQUEST
// part beside a plain body) into the Inbox through the normal append path, so its
// appointment is parsed on import exactly as a real inbound request, and returns its
// IMAP UID. The organizer is a foreign address, so accepting it queues no local
// notification error.
func seedMeetingRequest(t *testing.T, dir string) uint32 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const ics = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\nUID:eas-meeting-1\r\nSUMMARY:Quarterly Review\r\n" +
		"DTSTART:20260701T140000Z\r\nDTEND:20260701T150000Z\r\n" +
		"ORGANIZER:mailto:organizer@external.test\r\nATTENDEE:mailto:alice@hermex.test\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	raw := "From: organizer@external.test\r\nTo: alice@hermex.test\r\n" +
		"Subject: Quarterly Review\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nPlease attend.\r\n" +
		"--b\r\nContent-Type: text/calendar; method=REQUEST; charset=UTF-8\r\n\r\n" + ics +
		"--b--\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.UID
}
