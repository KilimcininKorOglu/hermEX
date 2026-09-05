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

// prefixAuth is a directory that also reports the operator's out-of-office
// subject prefix.
type prefixAuth struct {
	accounts directory.StaticAccounts
	prefix   string
	found    bool
}

func (a *prefixAuth) Authenticate(user, password string) (string, bool) {
	return a.accounts.Authenticate(user, password)
}

func (a *prefixAuth) GetAutoReplySettings() (directory.AutoReplySettings, bool, error) {
	return directory.AutoReplySettings{SubjectPrefix: a.prefix}, a.found, nil
}

// prefixHarness seeds a mailbox and returns the directory plus a signed-in
// browser.
func prefixHarness(t *testing.T) (*prefixAuth, requestFunc) {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	auth := &prefixAuth{accounts: directory.StaticAccounts{
		"alice@hermex.test": {Password: "pw", MailboxPath: dir},
	}}
	srv := NewServer(auth, auth.accounts, nil, "mail.hermex.test", []byte("vacation-prefix-secret"), "", false)
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
	return auth, do
}

// readVacation decodes the vacation endpoint's answer.
func readVacation(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestTheVacationFormIsToldTheOperatorsPrefix is what lets the SPA show what an
// empty subject will produce, instead of demanding one the protocols cannot
// store anyway.
func TestTheVacationFormIsToldTheOperatorsPrefix(t *testing.T) {
	auth, do := prefixHarness(t)
	auth.prefix, auth.found = "Otomatik yanıt", true

	got := readVacation(t, do(http.MethodGet, "/api/v1/vacation", ""))
	if got["subject_prefix"] != "Otomatik yanıt" {
		t.Errorf("subject_prefix = %v, want the operator's prefix", got["subject_prefix"])
	}

	// With nothing saved the form shows the wording the MTA actually falls back
	// to, not an empty string.
	auth.found = false
	got = readVacation(t, do(http.MethodGet, "/api/v1/vacation", ""))
	if got["subject_prefix"] != directory.DefaultAutoReplySubjectPrefix {
		t.Errorf("subject_prefix = %v, want the built-in default", got["subject_prefix"])
	}
}

// TestThePrefixIsNotTheUsersToSet keeps a client from reporting a fallback
// nobody configured. The value is the operator's, so the echo carries the stored
// one whatever the request said.
func TestThePrefixIsNotTheUsersToSet(t *testing.T) {
	auth, do := prefixHarness(t)
	auth.prefix, auth.found = "Otomatik yanıt", true

	got := readVacation(t, do(http.MethodPut, "/api/v1/vacation",
		`{"enabled":true,"subject":"","message":"away","subject_prefix":"Anything I like"}`))
	if got["subject_prefix"] != "Otomatik yanıt" {
		t.Errorf("subject_prefix = %v, want the stored prefix", got["subject_prefix"])
	}
	// And the saved settings still hold no subject, so the MTA composes one.
	got = readVacation(t, do(http.MethodGet, "/api/v1/vacation", ""))
	if got["subject"] != "" {
		t.Errorf("subject = %v, want the empty one that was saved", got["subject"])
	}
}
