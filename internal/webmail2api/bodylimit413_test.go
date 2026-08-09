package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
)

// oversizedServer builds an API server with a tiny body cap and a session cookie
// for a real mailbox, so a request reaches a handler that actually decodes a body.
func oversizedServer(t *testing.T, cap int64) (*Server, string) {
	t.Helper()
	box := t.TempDir()
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: box}}
	secret := []byte("body-limit-test-secret")
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: box, Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	SetMaxRequestBody(cap)
	t.Cleanup(func() { SetMaxRequestBody(0) })
	return srv, token
}

// post sends a body to an API path with the session cookie.
func postBody(t *testing.T, srv *Server, token, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestOversizedBodyAnswers413 is the defect. Every handler treats a failed body
// read like malformed JSON and answers 400, so a client that hit the size cap was
// told its request was broken. The two are different problems with different
// fixes, and only one of them is worth retrying with a smaller file.
func TestOversizedBodyAnswers413(t *testing.T) {
	srv, token := oversizedServer(t, 64)
	big := `{"folder":"inbox","file":"` + strings.Repeat("A", 4096) + `"}`

	for _, path := range []string{"/api/v1/mail/import", "/api/v1/mail/flag", "/api/v1/mail/send"} {
		rec := postBody(t, srv, token, path, big)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("POST %s = %d, want 413; body=%s", path, rec.Code, rec.Body.String())
			continue
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("POST %s body is not JSON: %s", path, rec.Body.String())
		} else if !strings.Contains(body["error"], "too large") {
			t.Errorf("POST %s error = %q, want it to name the size", path, body["error"])
		}
	}
}

// TestMalformedBodyStillAnswers400 is the negative control: only an overflow is
// rewritten, so a genuinely broken body keeps the status that describes it.
func TestMalformedBodyStillAnswers400(t *testing.T) {
	srv, token := oversizedServer(t, 1<<20)

	rec := postBody(t, srv, token, "/api/v1/mail/import", "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestAWellSizedBodyIsUntouched proves the wrapper does not disturb a normal
// response: a valid request still gets the handler's own status and body.
func TestAWellSizedBodyIsUntouched(t *testing.T) {
	srv, token := oversizedServer(t, 1<<20)

	rec := postBody(t, srv, token, "/api/v1/mail/import", `{"file":"","folder":"inbox"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want the handler's own 400 for an empty file", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), ".eml") {
		t.Errorf("body = %s, want the handler's own message", rec.Body.String())
	}
}

// TestTheImportCapAlsoAnswers413 covers the endpoint that installs a tighter cap
// of its own: without sharing the overflow state, its own reader would trip first
// and the answer would fall back to 400.
func TestTheImportCapAlsoAnswers413(t *testing.T) {
	srv, token := oversizedServer(t, 512<<20) // shared cap far above the import's own
	big := `{"folder":"inbox","file":"` + strings.Repeat("A", maxImportBytes+1024) + `"}`

	rec := postBody(t, srv, token, "/api/v1/mail/import", big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized import = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
}
