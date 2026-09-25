package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
)

// TestOtherMailboxReadsNeverProvision reads a local user who has no store yet
// through every path that opens someone else's mailbox: each answers "nothing
// here" and none creates the store.
func TestOtherMailboxReadsNeverProvision(t *testing.T) {
	bob := filepath.Join(t.TempDir(), "bob")
	accs := directory.StaticAccounts{"bob@hermex.test": {Password: "x", MailboxPath: bob}}
	srv := NewServer(accs, accs, nil, "mail.hermex.test", []byte("webmail2api-test-secret"), "", false)
	caller := sessionClaims{Email: "alice@hermex.test", Mailbox: t.TempDir()}

	now := time.Now()
	wantEq(t, "free/busy blocks", len(srv.busyFor(caller, "bob@hermex.test", now, now.Add(time.Hour))), 0)
	wantEq(t, "recall", srv.recallFromRecipient("bob@hermex.test", "<m@x>", "alice@hermex.test"), "unavailable")
	_, ok := srv.recipientCert("bob@hermex.test")
	wantEq(t, "recipient certificate found", ok, false)
	_, ok = srv.publishedCert("bob@hermex.test")
	wantEq(t, "published certificate found", ok, false)
	_, ok = srv.inboxTotal("bob@hermex.test")
	wantEq(t, "inbox total read", ok, false)

	rec := httptest.NewRecorder()
	srv.handleRecipientCert(rec, sessionRequest(t, srv, caller, "/api/v1/smime/recipient-cert?address=bob@hermex.test"))
	wantEq(t, "recipient-cert body", rec.Body.String(), "null\n")
	rec = httptest.NewRecorder()
	_, ok = openSharedStore(rec, directory.SharedMailbox{Address: "bob@hermex.test", StorePath: bob}, caller.Email)
	wantEq(t, "shared mailbox opened", ok, false)
	wantEq(t, "shared mailbox status", rec.Code, http.StatusNotFound)

	if _, err := os.Stat(bob); !os.IsNotExist(err) {
		t.Errorf("reading bob's mailbox created %s (stat err %v)", bob, err)
	}
}

// sessionRequest builds a GET carrying a valid session for claims.
func sessionRequest(t *testing.T, srv *Server, claims sessionClaims, target string) *http.Request {
	t.Helper()
	claims.Exp = time.Now().Add(time.Hour).Unix()
	token, err := mintToken(srv.secret, claims)
	mustNoErr(t, "mint session", err)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	return req
}
