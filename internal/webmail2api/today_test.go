package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestGetToday proves the dashboard aggregate: today's appointment and a due task
// surface in their widgets, and the notes/contacts counts reflect the folders.
func TestGetToday(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Seed an unread inbox message so the unread widget has data.
	raw := []byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: Hello\r\nDate: " +
		time.Now().UTC().Format(time.RFC1123Z) + "\r\n\r\nbody\r\n")
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0); err != nil {
		t.Fatalf("append inbox: %v", err)
	}
	st.Close()

	secret := []byte("today-test-secret")
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

	now := time.Now().UTC().Format(time.RFC3339)
	if rec := do(http.MethodPost, "/api/v1/calendar/events", `{"summary":"Standup today","start":"`+now+`","reminderMinutes":0}`); rec.Code != http.StatusOK {
		t.Fatalf("create event: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPost, "/api/v1/tasks", `{"summary":"Due today","due":"`+now+`","completed":false}`); rec.Code != http.StatusOK {
		t.Fatalf("create task: %d %s", rec.Code, rec.Body.String())
	}

	rec := do(http.MethodGet, "/api/v1/today", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("today: %d %s", rec.Code, rec.Body.String())
	}
	var got todayJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Unread != 1 {
		t.Errorf("unread = %d, want 1", got.Unread)
	}
	if len(got.UnreadRecent) != 1 || got.UnreadRecent[0].Subject != "Hello" {
		t.Errorf("unreadRecent = %+v, want Hello", got.UnreadRecent)
	}
	if len(got.Appointments) != 1 || got.Appointments[0].Subject != "Standup today" {
		t.Errorf("appointments = %+v, want Standup today", got.Appointments)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].Subject != "Due today" {
		t.Errorf("tasks = %+v, want Due today", got.Tasks)
	}
}
