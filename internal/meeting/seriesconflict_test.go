package meeting

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
)

// weeklySeries is a weekly 17:00 to 18:00 series of six instances from 2 March 2026.
const weeklySeries = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
	"BEGIN:VEVENT\r\nUID:standing-1\r\nSUMMARY:Standing\r\n" +
	"DTSTART:20260302T170000Z\r\nDTEND:20260302T180000Z\r\n" +
	"RRULE:FREQ=WEEKLY;COUNT=6\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// seedBusySeries stores a busy recurring appointment carrying the given iCalendar.
func seedBusySeries(t *testing.T, st *objectstore.Store, tags apptTags, body string) {
	t.Helper()
	first := time.Date(2026, 3, 2, 17, 0, 0, 0, time.UTC)
	props := mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"},
		{Tag: tags.uid, Value: "standing-1"},
		{Tag: tags.start, Value: mapi.UnixToNTTime(first)},
		{Tag: tags.end, Value: mapi.UnixToNTTime(first.Add(time.Hour))},
		{Tag: tags.busy, Value: int32(mapi.BusyBusy)},
		{Tag: tags.recur, Value: true},
	}
	if body != "" {
		props.Set(mapi.PrIcalOriginal, []byte(body))
	}
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: props}); err != nil {
		t.Fatal(err)
	}
}

// requestFor builds a meeting request occupying one hour from start.
func requestFor(tags apptTags, uid string, start time.Time) *oxcmail.Message {
	return &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: tags.uid, Value: uid},
		{Tag: tags.start, Value: mapi.UnixToNTTime(start)},
		{Tag: tags.end, Value: mapi.UnixToNTTime(start.Add(time.Hour))},
	}}
}

// conflictStore opens a mailbox and resolves the appointment tags.
func conflictStore(t *testing.T) (*objectstore.Store, apptTags) {
	t.Helper()
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustClose(t, st, "the store") })
	tags, err := resolveApptTags(st)
	if err != nil {
		t.Fatal(err)
	}
	return st, tags
}

// conflicts runs the conflict check, failing the test on a store error.
func conflicts(t *testing.T, st *objectstore.Store, tags apptTags, req *oxcmail.Message) bool {
	t.Helper()
	got, err := hasConflict(st, req, tags)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestSeriesInstanceIsAConflict is the load-bearing case: a request landing on a
// later instance of a standing meeting must not auto-accept into it. The series
// master carries its FIRST instance's time, so nothing but expansion finds it.
func TestSeriesInstanceIsAConflict(t *testing.T) {
	st, tags := conflictStore(t)
	seedBusySeries(t, st, tags, weeklySeries)

	// 16 March is the third instance, two weeks past the master's own start.
	req := requestFor(tags, "new-1", time.Date(2026, 3, 16, 17, 0, 0, 0, time.UTC))

	if !conflicts(t, st, tags, req) {
		t.Error("a request on a later instance of a series was not reported in conflict")
	}
}

// TestImportedSeriesIsAConflict stores the series the way CalDAV and webmail do,
// through the iCalendar import, rather than hand-setting the recurring flag. A
// series imported without PidLidRecurring was invisible to the check, so a
// meeting request on a later instance auto-accepted into a double booking.
func TestImportedSeriesIsAConflict(t *testing.T) {
	st, tags := conflictStore(t)
	msg, err := oxcical.Import([]byte(weeklySeries), oxcical.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		t.Fatal(err)
	}
	msg.Props.Set(tags.busy, int32(mapi.BusyBusy))
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), msg); err != nil {
		t.Fatal(err)
	}

	req := requestFor(tags, "new-1", time.Date(2026, 3, 16, 17, 0, 0, 0, time.UTC))
	if !conflicts(t, st, tags, req) {
		t.Error("a request on a later instance of an imported series was not reported in conflict")
	}
}

// TestMAPISeriesIsAConflict stores the series the way Outlook writes it over MAPI:
// the PidLidAppointmentRecur blob and no iCalendar copy. A request on a later
// instance is a conflict like one on an imported series; only the blob says the
// instance exists.
func TestMAPISeriesIsAConflict(t *testing.T) {
	st, tags := conflictStore(t)
	msg, err := oxcical.Import([]byte(weeklySeries), oxcical.Options{Resolver: st.GetNamedPropIDs})
	mustNoErr(t, err, "import the series")
	msg.Props.Remove(mapi.PrIcalOriginal)
	msg.Props.Set(tags.busy, int32(mapi.BusyBusy))
	_, err = st.CreateMessage(int64(mapi.PrivateFIDCalendar), msg)
	mustNoErr(t, err, "store the series")

	req := requestFor(tags, "new-1", time.Date(2026, 3, 16, 17, 0, 0, 0, time.UTC))
	if !conflicts(t, st, tags, req) {
		t.Error("a request on a later instance of a MAPI series was not reported in conflict")
	}
	free := requestFor(tags, "new-1", time.Date(2026, 3, 17, 17, 0, 0, 0, time.UTC))
	if conflicts(t, st, tags, free) {
		t.Error("a day the MAPI series does not occupy was reported in conflict")
	}
}

// TestFreeDayOfASeriesIsNoConflict proves the expansion is not a blanket refusal: a
// day the series does not occupy stays bookable.
func TestFreeDayOfASeriesIsNoConflict(t *testing.T) {
	st, tags := conflictStore(t)
	seedBusySeries(t, st, tags, weeklySeries)

	req := requestFor(tags, "new-1", time.Date(2026, 3, 17, 17, 0, 0, 0, time.UTC)) // a Tuesday

	if conflicts(t, st, tags, req) {
		t.Error("a day the series does not occupy was reported in conflict")
	}
}

// TestMovedInstanceConflictsAtItsNewTime proves the expansion honours an override:
// the moved instance blocks its new slot and frees its old one.
func TestMovedInstanceConflictsAtItsNewTime(t *testing.T) {
	st, tags := conflictStore(t)
	moved := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		"BEGIN:VEVENT\r\nUID:standing-1\r\nDTSTART:20260302T170000Z\r\nDTEND:20260302T180000Z\r\n" +
		"RRULE:FREQ=WEEKLY;COUNT=6\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:standing-1\r\nRECURRENCE-ID:20260309T170000Z\r\n" +
		"DTSTART:20260309T190000Z\r\nDTEND:20260309T200000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	seedBusySeries(t, st, tags, moved)

	old := requestFor(tags, "new-1", time.Date(2026, 3, 9, 17, 0, 0, 0, time.UTC))
	if conflicts(t, st, tags, old) {
		t.Error("the slot the instance moved away from is still reported busy")
	}
	now := requestFor(tags, "new-1", time.Date(2026, 3, 9, 19, 0, 0, 0, time.UTC))
	if !conflicts(t, st, tags, now) {
		t.Error("the slot the instance moved to is not reported busy")
	}
}

// TestSeriesDoesNotConflictWithItself proves an update to the standing meeting is
// still not a conflict with its own booking: it updates that appointment in place.
func TestSeriesDoesNotConflictWithItself(t *testing.T) {
	st, tags := conflictStore(t)
	seedBusySeries(t, st, tags, weeklySeries)

	req := requestFor(tags, "standing-1", time.Date(2026, 3, 16, 17, 0, 0, 0, time.UTC))

	if conflicts(t, st, tags, req) {
		t.Error("a series update was reported in conflict with its own booking")
	}
}

// TestUnexpandableSeriesStillBlocksItsOwnSlot stores busy series whose instances
// cannot be expanded: one written by a MAPI client, which keeps only the
// PidLidAppointmentRecur blob and no iCalendar copy, and one whose iCalendar body
// is damaged. Neither can be expanded here, but each still occupies the slot its
// own start and end name, so a request on that slot must not auto-accept into a
// double booking ([MS-OXOCAL] 3.1.4.6, a conflicting request is not accepted).
func TestUnexpandableSeriesStillBlocksItsOwnSlot(t *testing.T) {
	for name, body := range map[string]string{
		"no iCalendar copy": "",
		"damaged iCalendar": "BEGIN:VCALENDAR\r\nnot a calendar",
	} {
		t.Run(name, func(t *testing.T) {
			st, tags := conflictStore(t)
			seedBusySeries(t, st, tags, body)
			req := requestFor(tags, "new-1", time.Date(2026, 3, 2, 17, 0, 0, 0, time.UTC))
			if !conflicts(t, st, tags, req) {
				t.Error("a request on the series' own slot was not reported in conflict")
			}
		})
	}
}

// TestUnreadableSeriesIsAnError breaks the read that opens a series for
// expansion. The check cannot tell whether the series holds the slot, so it
// reports the failure, which leaves the request for the user instead of
// auto-accepting it into a possible double booking.
func TestUnreadableSeriesIsAnError(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	mustNoErr(t, err, "Open")
	t.Cleanup(func() { mustClose(t, st, "the store") })
	tags, err := resolveApptTags(st)
	mustNoErr(t, err, "resolve the tags")
	seedBusySeries(t, st, tags, weeklySeries)
	db, err := sql.Open("sqlite", filepath.Join(dir, "objects.sqlite3"))
	mustNoErr(t, err, "open the raw database")
	defer mustClose(t, db, "the raw database handle")
	if _, err := db.Exec(`ALTER TABLE recipients RENAME TO recipients_gone`); err != nil {
		t.Fatal(err)
	}

	req := requestFor(tags, "new-1", time.Date(2026, 3, 17, 17, 0, 0, 0, time.UTC))
	if busy, err := hasConflict(st, req, tags); err == nil {
		t.Errorf("hasConflict on an unreadable series = %v with no error", busy)
	}
}

// TestFreeSeriesIsNoConflict proves the busy status still decides: a series marked
// free blocks nothing.
func TestFreeSeriesIsNoConflict(t *testing.T) {
	st, tags := conflictStore(t)
	seedBusySeries(t, st, tags, weeklySeries)
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	if err != nil || len(objs) != 1 {
		t.Fatalf("seeded objects = %d (err %v), want 1", len(objs), err)
	}
	if err := st.ModifyMessageProperties(objs[0].ID, mapi.PropertyValues{
		{Tag: tags.busy, Value: int32(mapi.BusyFree)},
	}); err != nil {
		t.Fatal(err)
	}

	req := requestFor(tags, "new-1", time.Date(2026, 3, 16, 17, 0, 0, 0, time.UTC))

	if conflicts(t, st, tags, req) {
		t.Error("a series marked free was reported in conflict")
	}
}
