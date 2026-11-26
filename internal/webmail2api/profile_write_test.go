package webmail2api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// failingProfileDir is a directory whose profile writes fail, the way a database
// outage or a refused value does.
type failingProfileDir struct {
	directory.StaticAccounts
	propsErr, localeErr error
}

func (f failingProfileDir) SetUserProperties(string, map[uint32]string) (bool, error) {
	return false, f.propsErr
}

func (f failingProfileDir) SetUserLocale(string, string, string) (bool, error) {
	return false, f.localeErr
}

// putProfile sends PUT /api/v1/profile as alice, whose mailbox is mbox.
func putProfile(t *testing.T, srv *Server, secret []byte, mbox, body string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/profile", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestProfileSaveReportsAFailedWrite proves a profile change that was not stored
// is not answered as saved. Each field family has its own store (directory
// properties, the users row, the mailbox settings blob), and a failure in any of
// them must reach the user as an error, not a 200 carrying the old values.
func TestProfileSaveReportsAFailedWrite(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	secret := []byte("profile-write-secret")
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: mbox}}
	dir := failingProfileDir{StaticAccounts: accounts, propsErr: errors.New("db down"), localeErr: errors.New("db down")}
	srv := NewServer(dir, accounts, nil, "mail.hermex.test", secret, "", false)

	for _, body := range []string{`{"display_name":"Alice"}`, `{"timezone":"Europe/Istanbul"}`} {
		if rec := putProfile(t, srv, secret, mbox, body); rec.Code != http.StatusInternalServerError {
			t.Errorf("PUT %s with a failing directory = %d, want 500", body, rec.Code)
		}
	}
	// The onboarded flag lives in the mailbox; a missing mailbox cannot hold it.
	gone := filepath.Join(t.TempDir(), "missing")
	if rec := putProfile(t, srv, secret, gone, `{"onboarded":true}`); rec.Code != http.StatusInternalServerError {
		t.Errorf("PUT onboarded without a mailbox = %d, want 500", rec.Code)
	}
}
