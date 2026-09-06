package webmail2api

import (
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

	listReminders := func(what string) []reminderJSON {
		t.Helper()
		type listing struct {
			Reminders []reminderJSON `json:"reminders"`
		}
		return okBody[listing](t, what, do(http.MethodGet, "/api/v1/reminders", "")).Reminders
	}

	// An event whose start is in the past with a 15-minute reminder: its reminder
	// (start - 15m) is due now, so the list must surface it.
	past := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	wantStatus(t, "create event", do(http.MethodPost, "/api/v1/calendar/events",
		`{"summary":"Past standup","start":"`+past+`","reminderMinutes":15}`), http.StatusOK)

	// A future-dated event with a reminder must NOT appear (its reminder is not due yet).
	future := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
	wantStatus(t, "create future event", do(http.MethodPost, "/api/v1/calendar/events",
		`{"summary":"Future sync","start":"`+future+`","reminderMinutes":15}`), http.StatusOK)

	// A task with a reminder and a past due date appears too.
	wantStatus(t, "create task", do(http.MethodPost, "/api/v1/tasks",
		`{"summary":"Overdue task","due":"`+past+`","reminder":true,"completed":false}`), http.StatusOK)

	listed := listReminders("list reminders")
	if len(listed) != 2 {
		t.Fatalf("got %d reminders, want 2 (past appointment + overdue task)", len(listed))
	}
	apptID := reminderIDOfType(t, listed, "appointment")
	taskID := reminderIDOfType(t, listed, "task")

	// Dismiss the appointment reminder: it drops out of the next list, and the
	// stored event no longer carries PidLidReminderSet.
	wantStatus(t, "dismiss", do(http.MethodPost, "/api/v1/reminders/"+apptID+"/dismiss", ""), http.StatusOK)
	wantReminderGone(t, listReminders("list after dismiss"), apptID, "dismissed appointment")

	// Snooze the task reminder by 10 minutes: its due time moves past now, so it also
	// drops out of the list until the snooze elapses.
	wantStatus(t, "snooze", do(http.MethodPost, "/api/v1/reminders/"+taskID+"/snooze", `{"minutes":10}`), http.StatusOK)
	wantReminderGone(t, listReminders("list after snooze"), taskID, "snoozed task (due time did not advance)")
}

// reminderIDOfType returns the id of the one reminder of the given type,
// failing when the list carries none.
func reminderIDOfType(t *testing.T, list []reminderJSON, kind string) string {
	t.Helper()
	for _, r := range list {
		if r.Type == kind {
			return r.ID
		}
	}
	t.Fatalf("no %s reminder in the list", kind)
	return ""
}

// wantReminderGone fails when a reminder is still listed.
func wantReminderGone(t *testing.T, list []reminderJSON, id, what string) {
	t.Helper()
	for _, r := range list {
		if r.ID == id {
			t.Errorf("%s still in the reminder list", what)
		}
	}
}
