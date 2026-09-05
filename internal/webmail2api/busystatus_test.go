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

// TestEventBusyStatusRoundTripsWorkingElsewhere pins PidLidBusyStatus 4. An
// appointment migrated from Exchange commonly carries it, and iCalendar cannot
// express it (TRANSP only says opaque or transparent), so it survives only because
// the value is read and written as the named property directly.
func TestEventBusyStatusRoundTripsWorkingElsewhere(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	secret := []byte("busy-status-test-secret")
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

	const body = `{"summary":"Offsite","start":"2026-08-15T09:00:00Z","end":"2026-08-15T10:00:00Z","busyStatus":4}`
	if rec := do(http.MethodPost, "/api/v1/calendar/events", body); rec.Code != http.StatusOK {
		t.Fatalf("create: status %d body %s", rec.Code, rec.Body.String())
	}
	rec := do(http.MethodGet, "/api/v1/calendar/events", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d", rec.Code)
	}
	var out struct {
		Events []eventJSON `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(out.Events))
	}
	got := out.Events[0].BusyStatus
	if got == nil || *got != 4 {
		t.Errorf("busyStatus = %v, want 4 (working elsewhere)", got)
	}
}
