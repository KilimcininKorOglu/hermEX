package ews

import (
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxews"
)

const meetingRequestICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\n" +
	"BEGIN:VEVENT\r\nUID:meeting-42\r\nSUMMARY:Quarterly Review\r\nLOCATION:Boardroom\r\n" +
	"DTSTART:20260701T140000Z\r\nDTEND:20260701T150000Z\r\n" +
	"ORGANIZER:mailto:organizer@hermex.test\r\nATTENDEE:mailto:alice@hermex.test\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

// seedMeetingRequest synthesizes an inbound meeting request (an iTIP METHOD:REQUEST
// imported through oxcical, exactly as the future inbound path will) into the Inbox
// and returns the mailbox dir and the request's EWS ItemId.
func seedMeetingRequest(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	req, err := oxcical.Import([]byte(meetingRequestICS), oxcical.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	if v, _ := req.Props.Get(mapi.PrMessageClass); v != "IPM.Schedule.Meeting.Request" {
		st.Close()
		t.Fatalf("request class %v, want IPM.Schedule.Meeting.Request", v)
	}
	reqID, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), req)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()
	return dir, oxews.EncodeItemID(oxews.ItemID{FolderID: int64(mapi.PrivateFIDInbox), MessageID: reqID})
}

func meetingResponseReq(verb, refItemID string) string {
	return wrapRequest(`<CreateItem MessageDisposition="SaveOnly" xmlns="` + nsMessages + `">` +
		`<Items><t:` + verb + ` xmlns:t="` + nsTypes + `">` +
		`<t:ReferenceItemId Id="` + refItemID + `"/>` +
		`</t:` + verb + `></Items></CreateItem>`)
}

func meetingServer(t *testing.T, dir string) *httptest.Server {
	t.Helper()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts
}

// calendarLong reads a single PtLong named property off a calendar/message item.
func calendarLong(t *testing.T, st *objectstore.Store, msgID int64, name mapi.PropertyName) (int32, bool) {
	t.Helper()
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{name})
	if err != nil {
		t.Fatal(err)
	}
	tag := namedTag(ids[0], mapi.PtLong)
	pv, err := st.GetMessageProperties(msgID, tag)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := pv.Get(tag); ok {
		n, _ := v.(int32)
		return n, true
	}
	return 0, false
}

// TestMeetingResponseAccept proves accepting a meeting files the appointment in the
// Calendar as busy with an accepted response, and stamps the request as responded.
func TestMeetingResponseAccept(t *testing.T) {
	dir, itemID := seedMeetingRequest(t)
	ts := meetingServer(t, dir)

	_, out := soapPost(t, ts, meetingResponseReq("AcceptItem", itemID), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("AcceptItem not success: %s", out)
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cal, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	if err != nil {
		t.Fatal(err)
	}
	if len(cal) != 1 {
		t.Fatalf("calendar = %d items, want 1 (accepted appointment)", len(cal))
	}
	if busy, ok := calendarLong(t, st, cal[0].ID, mapi.NameBusyStatus); !ok || busy != busyBusy {
		t.Errorf("appointment busy = %d (ok=%v), want %d (busy)", busy, ok, busyBusy)
	}
	if resp, ok := calendarLong(t, st, cal[0].ID, mapi.NameResponseStatus); !ok || resp != respAccepted {
		t.Errorf("appointment response = %d (ok=%v), want %d (accepted)", resp, ok, respAccepted)
	}
	// the request itself is stamped responded
	reqID := decodeMID(t, itemID)
	if resp, ok := calendarLong(t, st, reqID, mapi.NameResponseStatus); !ok || resp != respAccepted {
		t.Errorf("request response stamp = %d (ok=%v), want %d (accepted)", resp, ok, respAccepted)
	}
}

// TestMeetingResponseDecline proves declining stamps the request declined but files
// no appointment — a meeting you declined does not belong on your calendar.
func TestMeetingResponseDecline(t *testing.T) {
	dir, itemID := seedMeetingRequest(t)
	ts := meetingServer(t, dir)

	_, out := soapPost(t, ts, meetingResponseReq("DeclineItem", itemID), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("DeclineItem not success: %s", out)
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cal, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	if err != nil {
		t.Fatal(err)
	}
	if len(cal) != 0 {
		t.Errorf("calendar = %d items, want 0 (a declined meeting files no appointment)", len(cal))
	}
	if resp, ok := calendarLong(t, st, decodeMID(t, itemID), mapi.NameResponseStatus); !ok || resp != respDeclined {
		t.Errorf("request response stamp = %d (ok=%v), want %d (declined)", resp, ok, respDeclined)
	}
}

// TestMeetingResponseTentativeDedup proves a tentative accept files a tentative
// appointment, and that re-responding (matched by iCalendar UID) updates that one
// appointment instead of filing a duplicate.
func TestMeetingResponseTentativeDedup(t *testing.T) {
	dir, itemID := seedMeetingRequest(t)
	ts := meetingServer(t, dir)

	if _, out := soapPost(t, ts, meetingResponseReq("TentativelyAcceptItem", itemID), true); !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("TentativelyAcceptItem not success: %s", out)
	}
	// re-respond with a firm accept: the same meeting must not duplicate.
	if _, out := soapPost(t, ts, meetingResponseReq("AcceptItem", itemID), true); !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("second response not success: %s", out)
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cal, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	if err != nil {
		t.Fatal(err)
	}
	if len(cal) != 1 {
		t.Fatalf("calendar = %d items, want 1 (re-response updates, not duplicates)", len(cal))
	}
	if busy, ok := calendarLong(t, st, cal[0].ID, mapi.NameBusyStatus); !ok || busy != busyBusy {
		t.Errorf("updated appointment busy = %d, want %d (the later accept)", busy, busyBusy)
	}
}

// decodeMID extracts the message id encoded in an EWS ItemId.
func decodeMID(t *testing.T, itemID string) int64 {
	t.Helper()
	id, err := oxews.DecodeItemID(itemID)
	if err != nil {
		t.Fatal(err)
	}
	return id.MessageID
}
