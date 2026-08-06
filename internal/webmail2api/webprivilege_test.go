package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// privAuth authenticates one account and reports the service privileges the
// operator set for it, standing in for the directory.
type privAuth struct {
	mailbox string
	privs   directory.ServicePrivileges
}

func (a privAuth) Authenticate(string, string) (string, bool) { return a.mailbox, true }
func (a privAuth) Privileges(string) (directory.ServicePrivileges, bool) {
	return a.privs, true
}

// plainAuth exposes no privileges at all, standing in for a directory that
// predates the capability.
type plainAuth struct{ mailbox string }

func (a plainAuth) Authenticate(string, string) (string, bool) { return a.mailbox, true }

// login posts correct credentials and returns the response recorder.
func login(t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader(`{"email":"alice@hermex.test","password":"right"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", body)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestLoginDeniedWithoutWebPrivilege proves the admin panel's per-user web switch
// actually bars webmail: the password is right, so this is an authorization
// refusal (403), and nothing is handed back that could authenticate a later
// request.
func TestLoginDeniedWithoutWebPrivilege(t *testing.T) {
	auth := privAuth{mailbox: t.TempDir(), privs: directory.ServicePrivileges{POP3IMAP: true, Web: false}}
	srv := NewServer(auth, directory.StaticAccounts{}, nil, "mail.hermex.test", []byte("web-priv-secret"), "", false)

	rec := login(t, srv)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("login without the web privilege = %d, want 403", rec.Code)
	}
	if got := rec.Header().Get("Set-Cookie"); got != "" {
		t.Errorf("a refused login set a cookie: %q", got)
	}
	if strings.Contains(rec.Body.String(), "token") {
		t.Errorf("a refused login returned a token: %s", rec.Body.String())
	}
}

// TestLoginAllowedWithWebPrivilege proves the check does not disturb the normal
// login of an account that holds the privilege.
func TestLoginAllowedWithWebPrivilege(t *testing.T) {
	auth := privAuth{mailbox: t.TempDir(), privs: directory.ServicePrivileges{Web: true}}
	srv := NewServer(auth, directory.StaticAccounts{}, nil, "mail.hermex.test", []byte("web-priv-secret"), "", false)

	if rec := login(t, srv); rec.Code != http.StatusOK {
		t.Fatalf("login with the web privilege = %d, want 200", rec.Code)
	}
}

// TestLoginAllowedWhenDirectoryHasNoPrivileges proves a directory that cannot
// report privileges still admits the login, so the check adds a restriction only
// where the operator can actually express one.
func TestLoginAllowedWhenDirectoryHasNoPrivileges(t *testing.T) {
	srv := NewServer(plainAuth{mailbox: t.TempDir()}, directory.StaticAccounts{}, nil, "mail.hermex.test", []byte("web-priv-secret"), "", false)

	if rec := login(t, srv); rec.Code != http.StatusOK {
		t.Fatalf("login against a privilege-less directory = %d, want 200", rec.Code)
	}
}
