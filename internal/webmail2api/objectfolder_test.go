package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/avtest"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// foreignSeed is one stored object a forged id names: its id, the folder it must
// stay in, and the subject it must keep.
type foreignSeed struct {
	id      int64
	folder  int64
	subject string
}

// seedForeignObjects stores a task, a note and an Inbox mail message, the
// objects the forged ids below name.
func seedForeignObjects(t *testing.T, mbox string) (task, note, mail foreignSeed) {
	t.Helper()
	st, err := objectstore.Open(mbox)
	mustNoErr(t, "open the mailbox", err)
	defer st.Close()
	create := func(fid int64, class, subject string) foreignSeed {
		id, err := st.CreateMessage(fid, &oxcmail.Message{Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: class}, {Tag: mapi.PrSubject, Value: subject},
		}})
		mustNoErr(t, "store "+subject, err)
		return foreignSeed{id: id, folder: fid, subject: subject}
	}
	task = create(mapi.PrivateFIDTasks, "IPM.Task", "Keep the task")
	note = create(mapi.PrivateFIDNotes, "IPM.StickyNote", "Keep the note")
	raw := []byte("From: a@b.test\r\nSubject: Keep the mail\r\n\r\nhi\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0)
	mustNoErr(t, "store the mail", err)
	mail = foreignSeed{id: info.ID, folder: int64(mapi.PrivateFIDInbox), subject: "Keep the mail"}
	return task, note, mail
}

// wantUntouched fails when a seeded object left its folder or lost its subject.
func wantUntouched(t *testing.T, mbox, route string, seed foreignSeed) {
	t.Helper()
	st, err := objectstore.Open(mbox)
	mustNoErr(t, "reopen the mailbox", err)
	defer st.Close()
	fid, err := st.MessageFolder(seed.id)
	if err != nil || fid != seed.folder {
		t.Errorf("%s: %q is in folder %d (err %v), want %d", route, seed.subject, fid, err, seed.folder)
		return
	}
	props, err := st.GetMessageProperties(seed.id, mapi.PrSubject)
	mustNoErr(t, "read the subject", err)
	wantEq(t, route+": the subject", propStr(props, mapi.PrSubject), seed.subject)
}

// TestCalendarIdsNameOnlyCalendars proves a calendar id from the client reaches
// only a calendar folder. The Inbox has a number too, so an event created or
// moved into it, or a calendar PATCH or DELETE naming it, must answer 404: the
// Inbox keeps its name and its mail, and no event lands in it.
func TestCalendarIdsNameOnlyCalendars(t *testing.T) {
	do, mbox := apiHarness(t)
	_, _, mail := seedForeignObjects(t, mbox)
	inbox := strconv.FormatInt(int64(mapi.PrivateFIDInbox), 10)
	wantStatus(t, "create in the default calendar", do(http.MethodPost, "/api/v1/calendar/events",
		`{"summary":"Standup","start":"2026-08-02T09:00:00Z"}`), http.StatusOK)
	event := listOneEvent(t, do, "list the event")

	cases := []struct{ name, method, target, body string }{
		{"create into the Inbox", http.MethodPost, "/api/v1/calendar/events",
			`{"summary":"Forged","start":"2026-08-03T09:00:00Z","calendarId":"` + inbox + `"}`},
		{"move into the Inbox", http.MethodPut, "/api/v1/calendar/events/" + event.UID,
			`{"summary":"Standup","start":"2026-08-02T09:00:00Z","calendarId":"` + inbox + `"}`},
		{"rename the Inbox", http.MethodPatch, "/api/v1/calendar/calendars/" + inbox, `{"name":"Renamed"}`},
		{"delete the Inbox", http.MethodDelete, "/api/v1/calendar/calendars/" + inbox, ""},
	}
	for _, c := range cases {
		wantEq(t, c.name+": the status", do(c.method, c.target, c.body).Code, http.StatusNotFound)
	}

	wantUntouched(t, mbox, "calendar ids", mail)
	st, err := objectstore.Open(mbox)
	mustNoErr(t, "reopen the mailbox", err)
	defer st.Close()
	props, err := st.GetFolderProperties(int64(mapi.PrivateFIDInbox), mapi.PrDisplayName)
	mustNoErr(t, "read the Inbox name", err)
	wantEq(t, "the Inbox name", propStr(props, mapi.PrDisplayName), "Inbox")
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list the Inbox objects", err)
	wantEq(t, "objects in the Inbox", len(objs), 1)
	wantEq(t, "events in the calendar", len(listEvents(t, do, "list after")), 1)
}

// TestWritesRefuseAnObjectOfAnotherFolder proves every webmail endpoint that
// writes an object by the id in its path reaches only objects of its own kind.
// The id comes from the client and the store addresses every object by it, so
// an event DELETE naming a task, or a contact PUT naming a mail message, must
// answer 404 and leave that object where it is with what it holds.
func TestWritesRefuseAnObjectOfAnotherFolder(t *testing.T) {
	withScanner(t, avtest.Clean)
	srv, token, mbox := importHarness(t)
	task, note, mail := seedForeignObjects(t, mbox)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	event := `{"summary":"Forged","start":"2026-08-02T09:00:00Z"}`
	move := `{"occurrence":"2026-08-02T09:00:00Z","start":"2026-08-02T10:00:00Z","end":"2026-08-02T11:00:00Z"}`
	routes := []struct {
		method, path, suffix, body string
		other                      foreignSeed
	}{
		{http.MethodPut, "/api/v1/calendar/events/", "", event, task},
		{http.MethodDelete, "/api/v1/calendar/events/", "", "", task},
		{http.MethodPut, "/api/v1/calendar/events/", "/occurrence", move, task},
		{http.MethodDelete, "/api/v1/calendar/events/", "/occurrence?at=2026-08-02T09:00:00Z", "", task},
		{http.MethodPut, "/api/v1/tasks/", "", `{"title":"Forged"}`, note},
		{http.MethodDelete, "/api/v1/tasks/", "", "", note},
		{http.MethodPut, "/api/v1/notes/", "", `{"title":"Forged"}`, task},
		{http.MethodDelete, "/api/v1/notes/", "", "", task},
		{http.MethodPut, "/api/v1/contacts/", "", `{"name":"Forged"}`, task},
		{http.MethodDelete, "/api/v1/contacts/", "", "", task},
		{http.MethodDelete, "/api/v1/contacts/", "/photo", "", task},
		{http.MethodPost, "/api/v1/reminders/", "/dismiss", "", note},
		{http.MethodPost, "/api/v1/reminders/", "/snooze", `{"minutes":5}`, note},
	}
	for _, r := range routes {
		for _, seed := range []foreignSeed{r.other, mail} {
			route := r.method + " " + r.path + "{" + seed.subject + "}" + r.suffix
			rec := do(r.method, r.path+strconv.FormatInt(seed.id, 10)+r.suffix, r.body)
			wantEq(t, route+": the status", rec.Code, http.StatusNotFound)
			wantUntouched(t, mbox, route, seed)
		}
	}
	for _, seed := range []foreignSeed{task, mail} {
		rec := putPhoto(t, srv, token, strconv.FormatInt(seed.id, 10), []byte("an ordinary photo"))
		wantEq(t, "PUT photo on "+seed.subject+": the status", rec.Code, http.StatusNotFound)
		wantUntouched(t, mbox, "PUT photo", seed)
	}
}
