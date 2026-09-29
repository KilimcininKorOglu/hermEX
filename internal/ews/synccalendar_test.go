package ews

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// calendarObjectID returns the message id of the calendar's one stored object.
func calendarObjectID(t *testing.T, st *objectstore.Store) int64 {
	t.Helper()
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	mustNoErr(t, "list the calendar", err)
	if len(objs) != 1 {
		t.Fatalf("the calendar holds %d objects, want 1", len(objs))
	}
	return objs[0].ID
}

// TestSyncFolderItemsReportsCalendarChanges proves a calendar synced over EWS
// reports an appointment as a CalendarItem when it is created, again when it is
// edited, and as a delete when it is removed. The calendar was diffed against the
// IMAP index, where appointments never are, so a syncing client saw none of them.
func TestSyncFolderItemsReportsCalendarChanges(t *testing.T) {
	ts, dir := seededWithMessage(t)
	importAppointment(t, dir,
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//test//EN",
		"BEGIN:VEVENT", "UID:sync-cal-1", "DTSTAMP:20260101T000000Z",
		"DTSTART:20261005T090000Z", "DTEND:20261005T100000Z", "SUMMARY:Synced",
		"END:VEVENT", "END:VCALENDAR")

	_, out := soapPost(t, ts, syncItemsReq("calendar", "", 0), true)
	if countChange(out, "Create") != 1 {
		t.Fatalf("the prime reports %d creates, want the appointment: %s", countChange(out, "Create"), out)
	}
	wantContains(t, "the created calendar item", out, "<Subject>Synced</Subject>")
	wantContains(t, "the created item type", out, "<CalendarItemType>Single</CalendarItemType>")
	state := syncStateRE.FindStringSubmatch(out)[1]

	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	id := calendarObjectID(t, st)
	mustNoErr(t, "edit the appointment", st.SetMessageProperties(id, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "Edited"}}))
	st.Close()

	_, out = soapPost(t, ts, syncItemsReq("calendar", state, 0), true)
	if countChange(out, "Update") != 1 || countChange(out, "Create") != 0 {
		t.Fatalf("the edit reports creates=%d updates=%d, want one update: %s", countChange(out, "Create"), countChange(out, "Update"), out)
	}
	wantContains(t, "the updated calendar item", out, "<Subject>Edited</Subject>")
	state = syncStateRE.FindStringSubmatch(out)[1]

	st, err = objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	mustNoErr(t, "delete the appointment", st.DeleteObject(id))
	st.Close()

	_, out = soapPost(t, ts, syncItemsReq("calendar", state, 0), true)
	if countChange(out, "Delete") != 1 {
		t.Fatalf("the removal reports %d deletes, want 1: %s", countChange(out, "Delete"), out)
	}
}
