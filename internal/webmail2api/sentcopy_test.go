package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// sentCopyHarness signs a mailbox owner in and returns a browser for the endpoint.
func sentCopyHarness(t *testing.T) requestFunc {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	accounts := directory.StaticAccounts{
		"alice@hermex.test": {Password: "pw", MailboxPath: dir},
	}
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", []byte("sent-copy-secret"), "", false)
	var jar []*http.Cookie
	do := requestFunc(func(method, target, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		for _, c := range jar {
			req.AddCookie(c)
		}
		srv.Handler().ServeHTTP(rec, req)
		if set := rec.Result().Cookies(); len(set) > 0 {
			jar = set
		}
		return rec
	})
	if rec := do(http.MethodPost, "/api/v1/auth/login",
		`{"email":"alice@hermex.test","password":"pw"}`); rec.Code != http.StatusOK {
		t.Fatalf("login = %d", rec.Code)
	}
	return do
}

// readSentCopy decodes the sent-copy endpoint's answer.
func readSentCopy(t *testing.T, rec *httptest.ResponseRecorder) sentCopyJSON {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out sentCopyJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSentCopySettingRoundTrips proves the mailbox owner can read and change both
// flags, and that a PUT carrying the whole object never drops the flag it did not
// change.
func TestSentCopySettingRoundTrips(t *testing.T) {
	do := sentCopyHarness(t)

	if got := readSentCopy(t, do(http.MethodGet, "/api/v1/account/sent-copy", "")); got.ForSendAs || got.ForSendOnBehalf {
		t.Errorf("an unconfigured mailbox reads as %+v, want both off", got)
	}

	for _, want := range []sentCopyJSON{
		{ForSendAs: true},
		{ForSendAs: true, ForSendOnBehalf: true},
		{ForSendOnBehalf: true},
		{},
	} {
		body, _ := json.Marshal(want)
		if got := readSentCopy(t, do(http.MethodPut, "/api/v1/account/sent-copy", string(body))); got != want {
			t.Errorf("PUT echoed %+v, want %+v", got, want)
		}
		if got := readSentCopy(t, do(http.MethodGet, "/api/v1/account/sent-copy", "")); got != want {
			t.Errorf("GET after PUT %+v read %+v", want, got)
		}
	}
}

// TestSentCopyNeedsASession keeps the setting the mailbox owner's: it decides where
// copies of mail sent in their name land, so an unauthenticated caller gets nothing.
func TestSentCopyNeedsASession(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: dir}}
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", []byte("sent-copy-secret"), "", false)

	for _, c := range []struct{ method, body string }{
		{http.MethodGet, ""},
		{http.MethodPut, `{"forSendAs":true}`},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(c.method, "/api/v1/account/sent-copy", strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a session = %d, want 401", c.method, rec.Code)
		}
	}
}
