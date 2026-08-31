package admin

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

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

// servedAdmin runs an admin server behind the real access-log middleware and
// returns its base URL plus the sink. Only a served daemon exercises the
// attribution: calling the handler directly skips the middleware entirely.
func servedAdmin(t *testing.T, d Directory) (string, *accessSink) {
	t.Helper()
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	srv.store = &fakeStore{}

	sink := &accessSink{}
	hs, err := serve.New("127.0.0.1:0", srv.Handler(), &config.Config{}, logging.New(sink), logging.Admin, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = hs.Start() }()
	t.Cleanup(func() { _ = hs.Shutdown(context.Background()) })
	return "http://" + hs.Addr().String(), sink
}

// adminLogin signs in over the served daemon and returns the session cookie.
func adminLogin(t *testing.T, base string) string {
	t.Helper()
	resp, err := http.Post(base+"/admin/login", "application/json",
		strings.NewReader(`{"login":"admin@hermex.test","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin login = %d, want 200", resp.StatusCode)
	}
	for _, sc := range resp.Header["Set-Cookie"] {
		if strings.HasPrefix(sc, sessionCookie+"=") {
			return cookieValue(sc, sessionCookie)
		}
	}
	t.Fatal("login set no session cookie")
	return ""
}

// TestAdminAccessLogNamesTheOperator proves an authenticated admin request is
// attributed to the operator. The panel authenticates with a cookie, so the
// middleware has no Basic header to read, and without the attribution the log
// cannot say who edited a user, granted a role or released a quarantined message.
func TestAdminAccessLogNamesTheOperator(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	base, sink := servedAdmin(t, d)
	session := adminLogin(t, base)

	req, err := http.NewRequest(http.MethodGet, base+"/admin/whoami", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami = %d, want 200; the request must be authenticated for the test to mean anything", resp.StatusCode)
	}

	user, ok := sink.userFor("/admin/whoami")
	if !ok {
		t.Fatal("no access-log event for the request")
	}
	if user != "admin@hermex.test" {
		t.Errorf("logged user = %q, want admin@hermex.test", user)
	}
}

// TestAdminAccessLogNamesAFailedLogin proves a rejected admin sign-in still names
// the login it was attempted against, the request an auditor reads first when
// investigating an attempt on the most privileged surface in the system.
func TestAdminAccessLogNamesAFailedLogin(t *testing.T) {
	base, sink := servedAdmin(t, &fakeDir{authOK: false})

	resp, err := http.Post(base+"/admin/login", "application/json",
		strings.NewReader(`{"login":"admin@hermex.test","password":"wrong"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login = %d, want 401", resp.StatusCode)
	}

	user, ok := sink.userFor("/admin/login")
	if !ok {
		t.Fatal("no access-log event for the failed login")
	}
	if user != "admin@hermex.test" {
		t.Errorf("logged user = %q, want the login the sign-in was attempted against", user)
	}
}
