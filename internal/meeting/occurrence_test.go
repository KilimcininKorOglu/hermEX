package meeting

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

const occurrenceUID = "series-42@hermex.test"

// seriesBody is the weekly series the attendee already accepted.
const seriesBody = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
	"BEGIN:VEVENT\r\nUID:" + occurrenceUID + "\r\nSUMMARY:Weekly\r\n" +
	"DTSTART:20260302T170000Z\r\nDTEND:20260302T180000Z\r\n" +
	"RRULE:FREQ=WEEKLY;COUNT=6\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// occurrenceBody moves the 9 March instance an hour later.
const occurrenceBody = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
	"BEGIN:VEVENT\r\nUID:" + occurrenceUID + "\r\nSUMMARY:Weekly moved\r\n" +
	"RECURRENCE-ID:20260309T170000Z\r\n" +
	"DTSTART:20260309T180000Z\r\nDTEND:20260309T190000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// occurrenceStore opens a mailbox and resolves the meeting tags.
func occurrenceStore(t *testing.T) (*objectstore.Store, Tags) {
	t.Helper()
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tags, err := ResolveTags(st)
	if err != nil {
		t.Fatal(err)
	}
	return st, tags
}

// seedSeries stores the accepted series appointment in the Calendar.
func seedSeries(t *testing.T, st *objectstore.Store, tags Tags) int64 {
	t.Helper()
	id := seedAppointmentFor(t, st, tags, occurrenceUID)
	if err := st.ModifyMessageProperties(id, mapi.PropertyValues{
		{Tag: mapi.PrIcalOriginal, Value: []byte(seriesBody)},
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// appendScheduling puts one scheduling message in the Inbox carrying an iCalendar
// body and the meeting's UID.
func appendScheduling(t *testing.T, st *objectstore.Store, tags Tags, body string) int64 {
	t.Helper()
	id := appendRequestWithUID(t, st, tags, occurrenceUID)
	if err := st.ModifyMessageProperties(id, mapi.PropertyValues{
		{Tag: mapi.PrIcalOriginal, Value: []byte(body)},
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// calendarICal reads a calendar item's verbatim iCalendar.
func calendarICal(t *testing.T, st *objectstore.Store, id int64) string {
	t.Helper()
	pv, err := st.GetMessageProperties(id, mapi.PrIcalOriginal)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := pv.Get(mapi.PrIcalOriginal)
	if !ok {
		t.Fatal("the calendar item carries no iCalendar body")
	}
	raw, _ := v.([]byte)
	return string(raw)
}

// TestOccurrenceUpdateKeepsTheSeries is the load-bearing case. An update for one
// instance carries the series UID, so filing it by UID alone overwrote the series
// with a single event. It must fold into the series as an override instead.
func TestOccurrenceUpdateKeepsTheSeries(t *testing.T) {
	st, tags := occurrenceStore(t)
	apptID := seedSeries(t, st, tags)
	reqID := appendScheduling(t, st, tags, occurrenceBody)

	if _, err := Respond(st, nil, nil, "alice@hermex.test", reqID, ResponseAccepted, false); err != nil {
		t.Fatal(err)
	}

	got := calendarICal(t, st, apptID)
	if !strings.Contains(got, "RRULE:FREQ=WEEKLY;COUNT=6") {
		t.Errorf("the series was overwritten by the occurrence:\n%s", got)
	}
	if !strings.Contains(got, "RECURRENCE-ID:20260309T170000Z") ||
		!strings.Contains(got, "DTSTART:20260309T180000Z") {
		t.Errorf("the moved instance did not land as an override:\n%s", got)
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDCalendar)); n != 1 {
		t.Errorf("Calendar holds %d items, want the one series object", n)
	}
}

// TestOccurrenceUpdateKeepsTheSeriesStart proves the series start is untouched: it
// is the property that moved the whole series when the occurrence overwrote the
// master.
func TestOccurrenceUpdateKeepsTheSeriesStart(t *testing.T) {
	st, tags := occurrenceStore(t)
	apptID := seedSeries(t, st, tags)
	start := mapi.UnixToNTTime(time.Date(2026, 3, 2, 17, 0, 0, 0, time.UTC))
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameAppointmentStartWhole})
	if err != nil {
		t.Fatal(err)
	}
	startProp := mapi.MakeTag(ids[0], mapi.PtSysTime)
	if err := st.ModifyMessageProperties(apptID, mapi.PropertyValues{{Tag: startProp, Value: start}}); err != nil {
		t.Fatal(err)
	}
	reqID := appendScheduling(t, st, tags, occurrenceBody)

	if _, err := Respond(st, nil, nil, "alice@hermex.test", reqID, ResponseAccepted, false); err != nil {
		t.Fatal(err)
	}

	pv, err := st.GetMessageProperties(apptID, startProp)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := pv.Get(startProp); v != start {
		t.Errorf("series start = %v, want it unchanged at %v", v, start)
	}
}

// TestOccurrenceUpdateWithoutASeriesFilesItsOwn proves an attendee invited to one
// instance alone still gets an appointment: there is nothing to fold into.
func TestOccurrenceUpdateWithoutASeriesFilesItsOwn(t *testing.T) {
	st, tags := occurrenceStore(t)
	reqID := appendScheduling(t, st, tags, occurrenceBody)

	apptID, err := Respond(st, nil, nil, "alice@hermex.test", reqID, ResponseAccepted, false)
	if err != nil {
		t.Fatal(err)
	}
	if n := folderCount(t, st, int64(mapi.PrivateFIDCalendar)); n != 1 {
		t.Fatalf("Calendar holds %d items, want the occurrence's own appointment", n)
	}
	if got := calendarICal(t, st, apptID); !strings.Contains(got, "RECURRENCE-ID:20260309T170000Z") {
		t.Errorf("the filed appointment is not the occurrence:\n%s", got)
	}
}

// TestDeclinedOccurrenceKeepsTheSeries proves declining one instance excludes that
// day instead of deleting the whole series, which is what matching the UID alone
// did.
func TestDeclinedOccurrenceKeepsTheSeries(t *testing.T) {
	st, tags := occurrenceStore(t)
	apptID := seedSeries(t, st, tags)
	reqID := appendScheduling(t, st, tags, occurrenceBody)

	if _, err := Respond(st, nil, nil, "alice@hermex.test", reqID, ResponseDeclined, false); err != nil {
		t.Fatal(err)
	}

	if n := folderCount(t, st, int64(mapi.PrivateFIDCalendar)); n != 1 {
		t.Fatalf("Calendar holds %d items, want the series to have survived the decline", n)
	}
	got := calendarICal(t, st, apptID)
	if !strings.Contains(got, "RRULE:FREQ=WEEKLY;COUNT=6") {
		t.Errorf("the series was deleted by an occurrence decline:\n%s", got)
	}
	if !strings.Contains(got, "EXDATE:20260309T170000Z") {
		t.Errorf("the declined instance was not excluded:\n%s", got)
	}
}

// TestDeclinedSeriesStillRemovesTheAppointment pins the unchanged case: declining
// the whole series takes it off the calendar.
func TestDeclinedSeriesStillRemovesTheAppointment(t *testing.T) {
	st, tags := occurrenceStore(t)
	seedSeries(t, st, tags)
	reqID := appendScheduling(t, st, tags, seriesBody)

	if _, err := Respond(st, nil, nil, "alice@hermex.test", reqID, ResponseDeclined, false); err != nil {
		t.Fatal(err)
	}

	if n := folderCount(t, st, int64(mapi.PrivateFIDCalendar)); n != 0 {
		t.Errorf("Calendar holds %d items, want the declined series removed", n)
	}
}
