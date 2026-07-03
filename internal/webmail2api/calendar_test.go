package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestCalendarEventUIDIsMessageID proves a listed event is identified by its store
// message id, not its iCalendar UID. The delete and update handlers parse the uid
// back to a store id, so a created-then-reloaded event must carry the numeric id;
// surfacing the string iCalendar UID (the meeting identity) would make delete and
// update fail to parse it, leaving the event uneditable after a refresh.
func TestCalendarEventUIDIsMessageID(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("calendar-uid-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// Create an event with no client-supplied uid (the SPA's normal path, which
	// makes the server mint an iCalendar UID).
	if rec := do(http.MethodPost, "/api/v1/calendar/events", `{"summary":"Standup","start":"2026-08-02T09:00:00Z"}`); rec.Code != http.StatusOK {
		t.Fatalf("create: status %d", rec.Code)
	}

	// Read it back the way the SPA does after a reload.
	rec := do(http.MethodGet, "/api/v1/calendar/events", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d", rec.Code)
	}
	var listed struct {
		Events []eventJSON `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(listed.Events))
	}
	uid := listed.Events[0].UID
	if _, err := strconv.ParseInt(uid, 10, 64); err != nil {
		t.Fatalf("listed event uid %q is not a numeric message id; delete/update would fail to parse it", uid)
	}

	// The delete handler parses that uid back to a store id; it must succeed (a
	// string iCalendar uid would 400 here).
	if rec := do(http.MethodDelete, "/api/v1/calendar/events/"+uid, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete by listed uid: status %d", rec.Code)
	}

	// The event is gone.
	rec = do(http.MethodGet, "/api/v1/calendar/events", "")
	var after struct {
		Events []eventJSON `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode list after delete: %v", err)
	}
	if len(after.Events) != 0 {
		t.Fatalf("event still present after delete: %d", len(after.Events))
	}
}

// TestCalendarEventReminderRoundTrip proves a reminder set in the SPA form
// survives a create-then-reload: the reminder lead time round-trips through
// oxcical's VALARM (NameReminderSet/NameReminderDelta named props) and comes
// back as the same reminderMinutes on the listed event. A silent loss here would
// make the form's reminder select a no-op after a refresh, so the assertion is on
// the exact minute value, not just presence.
func TestCalendarEventReminderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("calendar-reminder-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// Create a timed event with a 15-minute reminder.
	if rec := do(http.MethodPost, "/api/v1/calendar/events", `{"summary":"Standup","start":"2026-08-02T09:00:00Z","reminderMinutes":15}`); rec.Code != http.StatusOK {
		t.Fatalf("create with reminder: status %d", rec.Code)
	}

	// Reload it the way the SPA does; the reminder must come back as 15.
	rec := do(http.MethodGet, "/api/v1/calendar/events", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d", rec.Code)
	}
	var listed struct {
		Events []eventJSON `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(listed.Events))
	}
	got := listed.Events[0].ReminderMinutes
	if got == nil {
		t.Fatal("listed event has no reminderMinutes, want 15 (VALARM round-trip lost it)")
	}
	if *got != 15 {
		t.Fatalf("reminderMinutes = %d, want 15", *got)
	}

	// Clear the reminder by updating without one; the reloaded event must have none.
	uid := listed.Events[0].UID
	if rec := do(http.MethodPut, "/api/v1/calendar/events/"+uid, `{"summary":"Standup","start":"2026-08-02T09:00:00Z"}`); rec.Code != http.StatusOK {
		t.Fatalf("update clearing reminder: status %d", rec.Code)
	}
	rec = do(http.MethodGet, "/api/v1/calendar/events", "")
	var after struct {
		Events []eventJSON `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode list after update: %v", err)
	}
	if len(after.Events) != 1 {
		t.Fatalf("got %d events after update, want 1", len(after.Events))
	}
	if after.Events[0].ReminderMinutes != nil {
		t.Fatalf("reminderMinutes = %d after update without reminder, want nil", *after.Events[0].ReminderMinutes)
	}
}
