package webmail2api

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestFitsMAPILongPinsTheBoundary pins the width guard applied to JSON integers
// that land in 32-bit MAPI longs.
func TestFitsMAPILongPinsTheBoundary(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want bool
	}{
		{"zero", 0, true},
		{"ordinary", 3, true},
		{"int32 max", math.MaxInt32, true},
		{"int32 min", math.MinInt32, true},
		{"one past int32 max", math.MaxInt32 + 1, false},
		{"one past int32 min", math.MinInt32 - 1, false},
	}
	for _, tc := range cases {
		if got := fitsMAPILong(tc.in); got != tc.want {
			t.Errorf("%s: fitsMAPILong(%d) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestNoteColorRejectsWiderThanPtLong is the guard at a call site. JSON carries no
// integer width, so a color of 4294967297 used to wrap to 1 and the note came back
// green: a setting the client never chose, indistinguishable from one it did. The
// value is now dropped, so the note reads back as the yellow default.
func TestNoteColorRejectsWiderThanPtLong(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	secret := []byte("notes-width-test-secret")
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

	if rec := do(http.MethodPost, "/api/v1/notes", `{"title":"Wide","body":"x","color":4294967297}`); rec.Code != http.StatusOK {
		t.Fatalf("create: status %d body %s", rec.Code, rec.Body.String())
	}
	rec := do(http.MethodGet, "/api/v1/notes", "")
	var listed struct {
		Notes []noteJSON `json:"notes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Notes) != 1 {
		t.Fatalf("got %d notes, want 1", len(listed.Notes))
	}
	if got := listed.Notes[0].Color; got != 3 {
		t.Errorf("color = %d, want 3 (the yellow default; %d means the value wrapped)", got, got)
	}
}
