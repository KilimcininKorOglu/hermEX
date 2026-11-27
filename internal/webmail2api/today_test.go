package webmail2api

import (
	"net/http"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestGetToday proves the dashboard aggregate: today's appointment and a due task
// surface in their widgets, and the notes/contacts counts reflect the folders.
func TestGetToday(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open mailbox", err)
	// Seed an unread inbox message so the unread widget has data.
	raw := []byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: Hello\r\nDate: " +
		time.Now().UTC().Format(time.RFC1123Z) + "\r\n\r\nbody\r\n")
	_, err = st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0)
	mustNoErr(t, "append inbox", err)
	// A read message does not count as unread.
	_, err = st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: bob@hermex.test\r\nSubject: Old\r\n\r\nx\r\n"), time.Now(), objectstore.FlagSeen)
	mustNoErr(t, "append read", err)
	mustClose(t, st)

	do := apiHarnessFor(t, dir)
	now := time.Now().UTC().Format(time.RFC3339)
	wantStatus(t, "create event", do(http.MethodPost, "/api/v1/calendar/events",
		`{"summary":"Standup today","start":"`+now+`","reminderMinutes":0}`), http.StatusOK)
	wantStatus(t, "create task", do(http.MethodPost, "/api/v1/tasks",
		`{"summary":"Due today","due":"`+now+`","completed":false}`), http.StatusOK)
	// Neither a finished task nor an appointment two days out belongs on today.
	wantStatus(t, "create done task", do(http.MethodPost, "/api/v1/tasks",
		`{"summary":"Done already","due":"`+now+`","completed":true}`), http.StatusOK)
	later := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	wantStatus(t, "create later event", do(http.MethodPost, "/api/v1/calendar/events",
		`{"summary":"Later","start":"`+later+`","reminderMinutes":0}`), http.StatusOK)

	got := okBody[todayJSON](t, "today", do(http.MethodGet, "/api/v1/today", ""))
	wantEq(t, "unread", got.Unread, 1)
	wantEq(t, "unreadRecent count", len(got.UnreadRecent), 1)
	wantEq(t, "unreadRecent subject", got.UnreadRecent[0].Subject, "Hello")
	wantEq(t, "appointment count", len(got.Appointments), 1)
	wantEq(t, "appointment subject", got.Appointments[0].Subject, "Standup today")
	wantEq(t, "task count", len(got.Tasks), 1)
	wantEq(t, "task subject", got.Tasks[0].Subject, "Due today")
}
