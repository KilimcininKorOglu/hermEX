package webmail2api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
)

// openMailbox opens the harness mailbox for a direct look, closed with the test.
func openMailbox(t *testing.T, dir string) *objectstore.Store {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open mailbox", err)
	t.Cleanup(func() { st.Close() })
	return st
}

// createEvent stores an event through the API and returns its message id.
func createEvent(t *testing.T, do requestFunc, body string) int64 {
	t.Helper()
	uid := okBody[eventJSON](t, "create", do(http.MethodPost, "/api/v1/calendar/events", body)).UID
	id, err := strconv.ParseInt(uid, 10, 64)
	mustNoErr(t, "parse created id", err)
	return id
}

// namedTag resolves a named property the store already allocated.
func namedTag(t *testing.T, st *objectstore.Store, name mapi.PropertyName, typ mapi.PropType) mapi.PropTag {
	t.Helper()
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{name})
	mustNoErr(t, "resolve named property", err)
	return mapi.MakeTag(ids[0], typ)
}

// hobbies is PidTagHobbies (MS-OXPROPS), a property the calendar editor has no
// field for, standing in for what another client stored.
const hobbies = mapi.PropTag(0x3A43001F)

// seedForeignState gives a stored meeting what only other clients write: an
// unmodelled property, an attachment and Bob's accepted response.
func seedForeignState(t *testing.T, st *objectstore.Store, id int64) {
	t.Helper()
	mustNoErr(t, "set foreign property", st.SetMessageProperties(id, mapi.PropertyValues{{Tag: hobbies, Value: "chess"}}))
	_, _, err := st.CreateAttachment(id, mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
		{Tag: mapi.PrAttachLongFilename, Value: "agenda.txt"},
		{Tag: mapi.PrAttachDataBin, Value: []byte("agenda")},
	})
	mustNoErr(t, "attach", err)
	resp, err := responseStatusTag(st, true)
	mustNoErr(t, "response tag", err)
	recips, err := st.ListRecipients(id)
	mustNoErr(t, "list recipients", err)
	mustNoErr(t, "accept", st.SetRecipientProperties(recips[0].ID, mapi.PropertyValues{{Tag: resp, Value: int32(3)}}))
}

// TestEventEditKeepsTheSameObject edits a meeting another client has touched: the
// id, the meeting UID, the foreign property, the attachment and Bob's response
// stay, the cleared location goes, Carol joins, and nothing lands in the
// recoverable items.
func TestEventEditKeepsTheSameObject(t *testing.T) {
	do, dir := apiHarness(t)
	id := createEvent(t, do, `{"summary":"Review","start":"2026-08-02T09:00:00Z","end":"2026-08-02T10:00:00Z",`+
		`"location":"Room 1","timezone":"Europe/Istanbul","attendees":["bob@example.test"]}`)
	st := openMailbox(t, dir)
	seedForeignState(t, st, id)
	uidTag := namedTag(t, st, mapi.NameICalUID, mapi.PtUnicode)
	before, err := st.OpenMessage(id)
	mustNoErr(t, "open before", err)

	got := okBody[eventJSON](t, "edit", do(http.MethodPut, "/api/v1/calendar/events/"+strconv.FormatInt(id, 10),
		`{"summary":"Review 2","start":"2026-08-02T09:00:00Z","end":"2026-08-02T10:00:00Z",`+
			`"timezone":"Europe/Istanbul","attendees":["bob@example.test","carol@example.test"]}`))
	wantEq(t, "returned id", got.UID, strconv.FormatInt(id, 10))

	after, err := st.OpenMessage(id)
	mustNoErr(t, "open after", err)
	wantEq(t, "subject", propStr(after.Props, mapi.PrSubject), "Review 2")
	wantEq(t, "meeting uid", propStr(after.Props, uidTag), propStr(before.Props, uidTag))
	wantEq(t, "foreign property", propStr(after.Props, hobbies), "chess")
	wantEq(t, "attachments", len(after.Attachments), 1)
	wantEq(t, "location", after.Props.Has(namedTag(t, st, mapi.NameAppointmentLocation, mapi.PtUnicode)), false)
	wantAttendeeResponses(t, st, id, map[string]int32{"bob@example.test": 3, "carol@example.test": 0})
	deleted, err := st.ListAllSoftDeleted()
	mustNoErr(t, "list recoverable", err)
	wantEq(t, "recoverable items", len(deleted), 0)
}

// wantAttendeeResponses holds a meeting's attendees and their response statuses.
func wantAttendeeResponses(t *testing.T, st *objectstore.Store, id int64, want map[string]int32) {
	t.Helper()
	resp := namedTag(t, st, mapi.NameResponseStatus, mapi.PtLong)
	recips, err := st.ListRecipients(id)
	mustNoErr(t, "list recipients", err)
	wantEq(t, "attendee count", len(recips), len(want))
	for _, r := range recips {
		pv, err := st.GetRecipientProperties(r.ID, resp)
		mustNoErr(t, "read response", err)
		got, _ := propInt32(pv, resp)
		wantEq(t, "response of "+r.SmtpAddress, got, want[r.SmtpAddress])
	}
}

// TestEventEditMovesItBetweenCalendars moves an event into another calendar
// under the same id.
func TestEventEditMovesItBetweenCalendars(t *testing.T) {
	do, dir := apiHarness(t)
	id := createEvent(t, do, `{"summary":"Review","start":"2026-08-02T09:00:00Z"}`)
	cal := okBody[calendarJSON](t, "create calendar", do(http.MethodPost, "/api/v1/calendar/calendars", `{"name":"Work"}`))

	wantStatus(t, "move", do(http.MethodPut, "/api/v1/calendar/events/"+strconv.FormatInt(id, 10),
		`{"summary":"Review","start":"2026-08-02T09:00:00Z","calendarId":"`+cal.ID+`"}`), http.StatusOK)
	fid, err := openMailbox(t, dir).MessageFolder(id)
	mustNoErr(t, "folder", err)
	wantEq(t, "folder", strconv.FormatInt(fid, 10), cal.ID)
}

// TestSeriesEditKeepsExceptionsWhileThePatternHolds renames a series whose 31
// March instance was moved, then moves the series itself: the rename keeps the
// moved instance, the new start drops it.
func TestSeriesEditKeepsExceptionsWhileThePatternHolds(t *testing.T) {
	do, dir := apiHarness(t)
	id := createEvent(t, do, berlinStandup)
	st := openMailbox(t, dir)
	moveInstance(t, st, id)
	path := "/api/v1/calendar/events/" + strconv.FormatInt(id, 10)

	renamed := strings.Replace(berlinStandup, `"Standup"`, `"Daily sync"`, 1)
	wantStatus(t, "rename", do(http.MethodPut, path, renamed), http.StatusOK)
	wantEq(t, "override after rename", strings.Contains(string(icalOriginal(t, st, id)), "RECURRENCE-ID"), true)

	later := strings.Replace(berlinStandup, "2026-03-27T08:00:00Z", "2026-03-27T09:00:00Z", 1)
	wantStatus(t, "move series", do(http.MethodPut, path, later), http.StatusOK)
	wantEq(t, "override after moving the series", strings.Contains(string(icalOriginal(t, st, id)), "RECURRENCE-ID"), false)
}

// moveInstance moves the series' 31 March instance an hour later, the way a
// CalDAV client stores such an edit.
func moveInstance(t *testing.T, st *objectstore.Store, id int64) {
	t.Helper()
	moved := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:x\r\nSUMMARY:Standup\r\n" +
		"RECURRENCE-ID:20260331T070000Z\r\nDTSTART:20260331T080000Z\r\nDTEND:20260331T083000Z\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	merged, ok := oxcical.MergeOverride(icalOriginal(t, st, id), []byte(moved))
	if !ok {
		t.Fatal("MergeOverride refused the instance")
	}
	mustNoErr(t, "store override", st.ModifyMessageProperties(id, mapi.PropertyValues{{Tag: mapi.PrIcalOriginal, Value: merged}}))
}

// icalOriginal reads the stored iCalendar of a series.
func icalOriginal(t *testing.T, st *objectstore.Store, id int64) []byte {
	t.Helper()
	pv, err := st.GetMessageProperties(id, mapi.PrIcalOriginal)
	mustNoErr(t, "read original", err)
	v, _ := pv.Get(mapi.PrIcalOriginal)
	b, _ := v.([]byte)
	return b
}

// TestEventEditRefusesWhatIsNotAnEvent answers 404 for an unknown id and for a
// mail message, and leaves the message as it was.
func TestEventEditRefusesWhatIsNotAnEvent(t *testing.T) {
	do, dir := apiHarness(t)
	st := openMailbox(t, dir)
	info, err := st.AppendMessage(mapi.PrivateFIDInbox,
		[]byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: mail\r\n\r\nbody\r\n"), time.Now(), 0)
	mustNoErr(t, "append", err)

	for _, id := range []int64{info.ID, 1 << 40} {
		wantStatus(t, "edit", do(http.MethodPut, "/api/v1/calendar/events/"+strconv.FormatInt(id, 10),
			`{"summary":"x","start":"2026-08-02T09:00:00Z"}`), http.StatusNotFound)
	}
	msg, err := st.OpenMessage(info.ID)
	mustNoErr(t, "open mail", err)
	wantEq(t, "mail subject", propStr(msg.Props, mapi.PrSubject), "mail")
}
