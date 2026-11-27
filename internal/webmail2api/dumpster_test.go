package webmail2api

import (
	"net/http"
	"strconv"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// seedObject files one object of the given class in a folder and returns its id.
func seedObject(t *testing.T, dir string, fid int64, class string) int64 {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer mustClose(t, st)
	id, err := st.CreateMessage(fid, &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: class},
		{Tag: mapi.PrSubject, Value: "kept for recovery"},
	}})
	mustNoErr(t, "seed the object", err)
	return id
}

// wantRecoverable fails unless the object left the live store and sits in the
// Recoverable Items dumpster, from which a user can still restore it.
func wantRecoverable(t *testing.T, dir string, id int64, what string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer mustClose(t, st)
	items, err := st.ListAllSoftDeleted()
	mustNoErr(t, "list the dumpster", err)
	for _, it := range items {
		if it.Info.ID == id {
			return
		}
	}
	t.Errorf("%s: the deleted object %d is not in Recoverable Items, so it cannot be restored", what, id)
}

// TestWebmailDeletesAreRecoverable deletes a contact, a task, a note and an event
// through the webmail API. Every other client's delete of the same objects goes
// to Recoverable Items, and an operator restoring a user's mistake must find the
// item there whichever client the user deleted it from.
func TestWebmailDeletesAreRecoverable(t *testing.T) {
	cases := []struct {
		what, path, class string
		fid               int64
	}{
		{"contact", "/api/v1/contacts/", "IPM.Contact", int64(mapi.PrivateFIDContacts)},
		{"task", "/api/v1/tasks/", "IPM.Task", int64(mapi.PrivateFIDTasks)},
		{"note", "/api/v1/notes/", "IPM.StickyNote", int64(mapi.PrivateFIDNotes)},
		{"event", "/api/v1/calendar/events/", "IPM.Appointment", int64(mapi.PrivateFIDCalendar)},
	}
	for _, c := range cases {
		do, dir := apiHarness(t)
		id := seedObject(t, dir, c.fid, c.class)
		wantStatus(t, "delete the "+c.what, do(http.MethodDelete, c.path+strconv.FormatInt(id, 10), ""), http.StatusOK)
		wantRecoverable(t, dir, id, c.what)
	}
}

// TestADeleteThroughTheWrongKindDeletesNothing: a contact id sent to the task,
// note or event delete is refused, and the contact stays live and out of the
// dumpster; an unknown id deletes nothing either.
func TestADeleteThroughTheWrongKindDeletesNothing(t *testing.T) {
	do, dir := apiHarness(t)
	id := seedObject(t, dir, int64(mapi.PrivateFIDContacts), "IPM.Contact")
	sid := strconv.FormatInt(id, 10)
	for _, path := range []string{"/api/v1/tasks/" + sid, "/api/v1/notes/" + sid, "/api/v1/calendar/events/" + sid, "/api/v1/contacts/999999"} {
		if rec := do(http.MethodDelete, path, ""); rec.Code == http.StatusOK {
			t.Errorf("DELETE %s answered 200", path)
		}
	}
	st := openMailbox(t, dir)
	msg, err := st.OpenMessage(id)
	mustNoErr(t, "the contact after the refused deletes", err)
	wantEq(t, "class", propStr(msg.Props, mapi.PrMessageClass), "IPM.Contact")
	items, err := st.ListAllSoftDeleted()
	mustNoErr(t, "list the dumpster", err)
	wantEq(t, "dumpster after the refused deletes", len(items), 0)
}

// TestDeletingACalendarKeepsItsEventsRecoverable deletes a secondary calendar.
// The folder goes, but its events must reach Recoverable Items as a folder
// deleted from any other client's do.
func TestDeletingACalendarKeepsItsEventsRecoverable(t *testing.T) {
	do, dir := apiHarness(t)
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	parent := int64(mapi.PrivateFIDCalendar)
	fid, err := st.CreateFolder(&parent, "Team")
	mustNoErr(t, "create the calendar", err)
	mustNoErr(t, "mark it a calendar", st.SetFolderProperties(fid, mapi.PropertyValues{
		{Tag: mapi.PrContainerClass, Value: "IPF.Appointment"},
	}))
	mustClose(t, st)
	id := seedObject(t, dir, fid, "IPM.Appointment")

	wantStatus(t, "delete the calendar",
		do(http.MethodDelete, "/api/v1/calendar/calendars/"+strconv.FormatInt(fid, 10), ""), http.StatusOK)
	wantRecoverable(t, dir, id, "event of the deleted calendar")
}
