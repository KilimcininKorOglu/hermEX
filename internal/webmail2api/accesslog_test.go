package webmail2api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/serve"
)

// accessSink keeps the access-log events a served request produced.
type accessSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (c *accessSink) Write(e logging.Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

// userFor returns the acting account recorded for the first request to path.
func (c *accessSink) userFor(path string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.Name == "http.request" && e.Fields["path"] == path {
			return e.User, true
		}
	}
	return "", false
}

// servedWebmail runs a webmail server behind the real access-log middleware and
// returns its base URL plus the sink. Only a served daemon exercises the
// attribution: calling the handler directly skips the middleware entirely.
func servedWebmail(t *testing.T) (string, *accessSink, []byte, string) {
	t.Helper()
	mbox := t.TempDir()
	secret := []byte("webmail-accesslog-secret")
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: mbox}}
	api := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)

	sink := &accessSink{}
	hs, err := serve.New("127.0.0.1:0", api.Handler(), &config.Config{}, logging.New(sink), logging.Webmail, nil)
	if err != nil {
		t.Fatal(err)
	}
	go hs.Start()
	t.Cleanup(func() { hs.Shutdown(context.Background()) })
	return "http://" + hs.Addr().String(), sink, secret, mbox
}

// TestWebmailAccessLogNamesTheSessionUser proves an authenticated webmail request
// is attributed to the account. Webmail authenticates with a cookie, so the
// middleware has no Basic header to read, and without the attribution its
// password changes, delegate grants and mail sends are logged against nobody.
func TestWebmailAccessLogNamesTheSessionUser(t *testing.T) {
	base, sink, secret, mbox := servedWebmail(t)
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, base+"/api/v1/folders", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("folders = %d, want 200; the request must be authenticated for the test to mean anything", resp.StatusCode)
	}

	user, ok := sink.userFor("/api/v1/folders")
	if !ok {
		t.Fatal("no access-log event for the request")
	}
	if user != "alice@hermex.test" {
		t.Errorf("logged user = %q, want alice@hermex.test", user)
	}
}

// TestWebmailAccessLogNamesAFailedLogin proves a rejected sign-in still names the
// account it was attempted against. That request carries no session at all, and it
// is the one an auditor investigating a break-in attempt reads first.
func TestWebmailAccessLogNamesAFailedLogin(t *testing.T) {
	base, sink, _, _ := servedWebmail(t)

	resp, err := http.Post(base+"/api/v1/auth/login", "application/json",
		strings.NewReader(`{"email":"alice@hermex.test","password":"wrong"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login = %d, want 401", resp.StatusCode)
	}

	user, ok := sink.userFor("/api/v1/auth/login")
	if !ok {
		t.Fatal("no access-log event for the failed login")
	}
	if user != "alice@hermex.test" {
		t.Errorf("logged user = %q, want the account the sign-in was attempted against", user)
	}
}
