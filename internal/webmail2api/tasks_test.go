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

	body := `{"summary":"Ship report","description":"Q3 numbers","start":"2026-07-01","due":"2026-07-15","status":1,"percent":40,"priority":2,"reminder":true,"categories":["Urgent","Finance"]}`
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
		"percent": itoa(c.Percent),
	}
	want := map[string]string{
		"summary": "Ship report", "description": "Q3 numbers", "start": "2026-07-01",
		"due": "2026-07-15", "priority": "2", "status": "1", "percent": "40",
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
