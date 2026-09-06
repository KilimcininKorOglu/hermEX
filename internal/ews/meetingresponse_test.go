package ews

import (
	"bytes"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/meeting"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxews"
	"hermex/internal/relay"
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
	return meetingResponseReqDisp(verb, refItemID, "SaveOnly")
}

func meetingResponseReqDisp(verb, refItemID, disp string) string {
	return wrapRequest(`<CreateItem MessageDisposition="` + disp + `" xmlns="` + nsMessages + `">` +
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
	mustNoErr(t, "open the store", err)
	defer st.Close()
	cal, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	mustNoErr(t, "list the calendar", err)
	if len(cal) != 1 {
		t.Fatalf("calendar = %d items, want 1 (accepted appointment)", len(cal))
	}
	wantCalendarLong(t, st, "the appointment busy status", cal[0].ID, mapi.NameBusyStatus, 2)
	wantCalendarLong(t, st, "the appointment response", cal[0].ID, mapi.NameResponseStatus, meeting.ResponseAccepted)
	// the request itself is stamped responded
	wantCalendarLong(t, st, "the request response stamp", decodeMID(t, itemID), mapi.NameResponseStatus, meeting.ResponseAccepted)
}

// wantCalendarLong checks one calendar named property's integer value.
func wantCalendarLong(t *testing.T, st *objectstore.Store, what string, id int64, name mapi.PropertyName, want int32) {
	t.Helper()
	got, ok := calendarLong(t, st, id, name)
	if !ok {
		t.Errorf("%s: the property is absent", what)
		return
	}
	wantEq(t, what, got, want)
}

// TestMeetingResponseDecline proves declining stamps the request declined but files
// no appointment, a meeting you declined does not belong on your calendar.
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
	if resp, ok := calendarLong(t, st, decodeMID(t, itemID), mapi.NameResponseStatus); !ok || resp != meeting.ResponseDeclined {
		t.Errorf("request response stamp = %d (ok=%v), want %d (declined)", resp, ok, meeting.ResponseDeclined)
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
	if busy, ok := calendarLong(t, st, cal[0].ID, mapi.NameBusyStatus); !ok || busy != int32(2) {
		t.Errorf("updated appointment busy = %d, want %d (the later accept)", busy, int32(2))
	}
}

// TestMeetingResponseNotifiesOrganizer proves a SendAndSaveCopy accept notifies the
// organizer with an iTIP REPLY: the response is routed (here, to a foreign-domain
// organizer, so it queues for relay) as the responder, carrying METHOD:REPLY with
// the attendee's ACCEPTED participation status.
func TestMeetingResponseNotifiesOrganizer(t *testing.T) {
	dir := t.TempDir()
	reqID := seedExternalMeetingRequest(t, dir)

	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	mustNoErr(t, "open the relay spool", err)
	defer sp.Close()
	srv.Spool = sp
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	itemID := oxews.EncodeItemID(oxews.ItemID{FolderID: int64(mapi.PrivateFIDInbox), MessageID: reqID})
	_, out := soapPost(t, ts, meetingResponseReqDisp("AcceptItem", itemID, "SendAndSaveCopy"), true)
	wantContains(t, "the AcceptItem response class", out, `ResponseClass="Success"`)

	due, err := sp.Claim(time.Now(), 10)
	mustNoErr(t, "claim the spooled reply", err)
	if len(due) != 1 {
		t.Fatalf("relay spool = %v, want one queued reply", due)
	}
	wantEq(t, "the reply recipient", due[0].Recipient, "boss@external.test")
	wantEq(t, "the reply envelope From", due[0].From, testUser)
	if !bytes.Contains(due[0].Body, []byte("METHOD:REPLY")) || !bytes.Contains(due[0].Body, []byte("PARTSTAT=ACCEPTED")) {
		t.Errorf("reply body is not an iTIP REPLY accept:\n%s", due[0].Body)
	}
}

// seedExternalMeetingRequest files a meeting request from an external organizer
// in the mailbox's Inbox and returns its message id.
func seedExternalMeetingRequest(t *testing.T, dir string) int64 {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	const ics = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\nUID:meeting-99\r\nSUMMARY:Budget\r\n" +
		"DTSTART:20260701T140000Z\r\nDTEND:20260701T150000Z\r\n" +
		`ORGANIZER;CN="The Boss":mailto:boss@external.test` + "\r\n" +
		"ATTENDEE:mailto:alice@hermex.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	req, err := oxcical.Import([]byte(ics), oxcical.Options{Resolver: st.GetNamedPropIDs})
	mustNoErr(t, "import the meeting request", err)
	reqID, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), req)
	mustNoErr(t, "store the meeting request", err)
	return reqID
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
