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

// TestTaskRichFieldsRoundTrip proves the task's start date, priority, reminder,
// and categories survive a create-then-reload through the oxtask named-property
// model, so the form is not a no-op after a refresh. Importance (PR_IMPORTANCE)
// maps 0=low, 1=normal, 2=high; the SPA sends 2 (high) and reads it back.
func TestTaskRichFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("tasks-rich-test-secret")
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

	body := `{"summary":"Ship report","description":"Q3 numbers","start":"2026-07-01","due":"2026-07-15","status":1,"percent":40,"priority":2,"reminder":true,"categories":["Urgent","Finance"],"recurrence":"FREQ=WEEKLY;INTERVAL=2;COUNT=5"}`
	if rec := do(http.MethodPost, "/api/v1/tasks", body); rec.Code != http.StatusOK {
		t.Fatalf("create: status %d body %s", rec.Code, rec.Body.String())
	}

	rec := do(http.MethodGet, "/api/v1/tasks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d", rec.Code)
	}
	var listed struct {
		Tasks []taskJSON `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(listed.Tasks))
	}
	c := listed.Tasks[0]
	checks := map[string]string{
		"summary": c.Summary, "description": c.Description, "start": c.Start,
		"due": c.Due, "priority": itoa(c.Priority), "status": itoa(c.Status),
		"percent": itoa(c.Percent), "recurrence": c.Recurrence,
	}
	want := map[string]string{
		"summary": "Ship report", "description": "Q3 numbers", "start": "2026-07-01",
		"due": "2026-07-15", "priority": "2", "status": "1", "percent": "40",
		"recurrence": "FREQ=WEEKLY;INTERVAL=2;COUNT=5",
	}
	for k, w := range want {
		if checks[k] != w {
			t.Errorf("%s = %q, want %q", k, checks[k], w)
		}
	}
	if !c.Reminder {
		t.Errorf("reminder = false, want true")
	}
	if len(c.Categories) != 2 || c.Categories[0] != "Urgent" || c.Categories[1] != "Finance" {
		t.Errorf("categories = %v, want [Urgent Finance]", c.Categories)
	}
}

// TestTaskAssignmentRoundTrip proves the assignment spine (Owner, Assigner,
// AcceptanceState) survives a create-then-reload through the oxtask named-property
// model, so a task assigned in webmail reaches EAS/EWS/MAPI with the same owner and
// acceptance state instead of a webmail-only field.
func TestTaskAssignmentRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("tasks-assign-test-secret")
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

	// Alice assigns her task to bob; acceptance starts unknown (1).
	body := `{"summary":"Review PR","owner":"bob@hermex.test","assigner":"alice@hermex.test","acceptState":1,"completed":false}`
	if rec := do(http.MethodPost, "/api/v1/tasks", body); rec.Code != http.StatusOK {
		t.Fatalf("create: status %d body %s", rec.Code, rec.Body.String())
	}

	rec := do(http.MethodGet, "/api/v1/tasks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d", rec.Code)
	}
	var listed struct {
		Tasks []taskJSON `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(listed.Tasks))
	}
	c := listed.Tasks[0]
	if c.Owner != "bob@hermex.test" {
		t.Errorf("Owner = %q, want bob@hermex.test", c.Owner)
	}
	if c.Assigner != "alice@hermex.test" {
		t.Errorf("Assigner = %q, want alice@hermex.test", c.Assigner)
	}
	if c.AcceptState != 1 {
		t.Errorf("AcceptState = %d, want 1 (unknown)", c.AcceptState)
	}

	// Bob accepts the task: the owner stays bob, acceptance becomes 2.
	update := `{"summary":"Review PR","owner":"bob@hermex.test","assigner":"alice@hermex.test","acceptState":2,"completed":false}`
	upd := do(http.MethodPut, "/api/v1/tasks/"+c.UID, update)
	if upd.Code != http.StatusOK {
		t.Fatalf("update: status %d body %s", upd.Code, upd.Body.String())
	}
	var after taskJSON
	if err := json.Unmarshal(upd.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if after.AcceptState != 2 {
		t.Errorf("after AcceptState = %d, want 2 (accepted)", after.AcceptState)
	}
	if after.Owner != "bob@hermex.test" {
		t.Errorf("after Owner = %q, want bob@hermex.test", after.Owner)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	if n < 0 {
		return "-?"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
