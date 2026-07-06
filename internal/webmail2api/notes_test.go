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

// TestNoteColorRoundTrip proves a sticky note's color (PidLidNoteColor) survives
// a create-then-reload cycle, and that a note with no color defaults to yellow
// (3, the Outlook default).
func TestNoteColorRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("notes-color-test-secret")
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

	// Create a green (1) note and a default (no color) note.
	if rec := do(http.MethodPost, "/api/v1/notes", `{"title":"Groceries","body":"Milk","color":1}`); rec.Code != http.StatusOK {
		t.Fatalf("create green: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPost, "/api/v1/notes", `{"title":"Idea","body":"Ship it"}`); rec.Code != http.StatusOK {
		t.Fatalf("create default: status %d body %s", rec.Code, rec.Body.String())
	}

	rec := do(http.MethodGet, "/api/v1/notes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d", rec.Code)
	}
	var listed struct {
		Notes []noteJSON `json:"notes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed.Notes) != 2 {
		t.Fatalf("got %d notes, want 2", len(listed.Notes))
	}

	byTitle := map[string]int{}
	for _, n := range listed.Notes {
		byTitle[n.Title] = n.Color
	}
	if byTitle["Groceries"] != 1 {
		t.Errorf("Groceries color = %d, want 1 (green)", byTitle["Groceries"])
	}
	if byTitle["Idea"] != 3 {
		t.Errorf("Idea color = %d, want 3 (yellow default)", byTitle["Idea"])
	}
}
