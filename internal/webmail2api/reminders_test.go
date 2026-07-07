package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestRemindersListDismissSnooze proves the reminder backend the reference
// reminderlistmodule/reminderitemmodule expose: due reminders across the calendar
// (and Tasks) folders are listed, dismiss clears the flag, snooze advances the time.
func TestRemindersListDismissSnooze(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("reminders-test-secret")
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

	// An event whose start is in the past with a 15-minute reminder: its reminder
	// (start - 15m) is due now, so the list must surface it.
	past := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	if rec := do(http.MethodPost, "/api/v1/calendar/events", `{"summary":"Past standup","start":"`+past+`","reminderMinutes":15}`); rec.Code != http.StatusOK {
		t.Fatalf("create event: %d %s", rec.Code, rec.Body.String())
	}

	// A future-dated event with a reminder must NOT appear (its reminder is not due yet).
	future := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
	if rec := do(http.MethodPost, "/api/v1/calendar/events", `{"summary":"Future sync","start":"`+future+`","reminderMinutes":15}`); rec.Code != http.StatusOK {
		t.Fatalf("create future event: %d %s", rec.Code, rec.Body.String())
	}

	// A task with a reminder and a past due date appears too.
	if rec := do(http.MethodPost, "/api/v1/tasks", `{"summary":"Overdue task","due":"`+past+`","reminder":true,"completed":false}`); rec.Code != http.StatusOK {
		t.Fatalf("create task: %d %s", rec.Code, rec.Body.String())
	}

	rec := do(http.MethodGet, "/api/v1/reminders", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list reminders: %d %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Reminders []reminderJSON `json:"reminders"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed.Reminders) != 2 {
		t.Fatalf("got %d reminders, want 2 (past appointment + overdue task)", len(listed.Reminders))
	}
	var apptID, taskID string
	for i := range listed.Reminders {
		switch listed.Reminders[i].Type {
		case "appointment":
			apptID = listed.Reminders[i].ID
		case "task":
			taskID = listed.Reminders[i].ID
		}
	}
	if apptID == "" {
		t.Fatal("no appointment reminder in the list")
	}
	if taskID == "" {
		t.Fatal("no task reminder in the list")
	}

	// Dismiss the appointment reminder: it drops out of the next list, and the
	// stored event no longer carries PidLidReminderSet.
	if rec := do(http.MethodPost, "/api/v1/reminders/"+apptID+"/dismiss", ""); rec.Code != http.StatusOK {
		t.Fatalf("dismiss: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(http.MethodGet, "/api/v1/reminders", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &listed)
	for _, rem := range listed.Reminders {
		if rem.ID == apptID {
			t.Error("dismissed appointment still in the reminder list")
		}
	}

	// Snooze the task reminder by 10 minutes: its due time moves past now, so it also
	// drops out of the list until the snooze elapses.
	if rec := do(http.MethodPost, "/api/v1/reminders/"+taskID+"/snooze", `{"minutes":10}`); rec.Code != http.StatusOK {
		t.Fatalf("snooze: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(http.MethodGet, "/api/v1/reminders", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &listed)
	for _, rem := range listed.Reminders {
		if rem.ID == taskID {
			t.Error("snoozed task still in the reminder list (due time did not advance)")
		}
	}
}
