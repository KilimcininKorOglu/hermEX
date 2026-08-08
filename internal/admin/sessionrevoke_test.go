package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// sessionDir is a directory that also keeps admin sessions, so a test can prove a
// signed-out or password-changed token stops working. fakeDir alone implements no
// session store, which is the stateless deployment the server still supports.
type sessionDir struct {
	*fakeDir
	sessions map[string]directory.AdminSession
}

func newSessionDir(d *fakeDir) *sessionDir {
	return &sessionDir{fakeDir: d, sessions: map[string]directory.AdminSession{}}
}

func (s *sessionDir) CreateAdminSession(sess directory.AdminSession) error {
	s.sessions[sess.Jti] = directory.AdminSession{
		Jti: sess.Jti, Login: strings.ToLower(sess.Login),
		CreatedAt: sess.CreatedAt, ExpiresAt: sess.ExpiresAt,
	}
	return nil
}

func (s *sessionDir) AdminSessionActive(jti string, now int64) (bool, error) {
	sess, ok := s.sessions[jti]
	return ok && sess.ExpiresAt > now, nil
}

func (s *sessionDir) DeleteAdminSession(login, jti string) error {
	if sess, ok := s.sessions[jti]; ok && sess.Login == strings.ToLower(login) {
		delete(s.sessions, jti)
	}
	return nil
}

func (s *sessionDir) DeleteAdminSessionsFor(login string) error {
	for jti, sess := range s.sessions {
		if sess.Login == strings.ToLower(login) {
			delete(s.sessions, jti)
		}
	}
	return nil
}

// sessionServer builds an admin server over a revocation-capable directory.
func sessionServer(t *testing.T) (*httptest.Server, *sessionDir) {
	t.Helper()
	d := newSessionDir(&fakeDir{authOK: true, password: "pw", uid: 7,
		roles: []directory.AdminRole{{Role: directory.AdminSystem}}})
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	srv.store = &fakeStore{}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, d
}

// whoamiWith calls an authenticated endpoint with the given session cookie.
func whoamiWith(t *testing.T, ts *httptest.Server, session string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/admin/whoami", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestAdminLogoutRevokesTheSession proves signing out ends the session on the
// server, not only in the browser. The token is self-signed and valid for eight
// hours, so clearing the cookie alone left anyone holding a captured copy with a
// working session for the rest of that window.
func TestAdminLogoutRevokesTheSession(t *testing.T) {
	ts, _ := sessionServer(t)
	session, csrf := loginCookies(t, ts)

	if code := whoamiWith(t, ts, session); code != http.StatusOK {
		t.Fatalf("whoami before logout = %d, want 200", code)
	}
	resp := authedReq(t, ts, "POST", "/admin/logout", session, csrf, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", resp.StatusCode)
	}
	// The very same token, replayed as a captured cookie would be.
	if code := whoamiWith(t, ts, session); code != http.StatusUnauthorized {
		t.Errorf("whoami after logout = %d, want 401; the signed-out token still works", code)
	}
}

// TestAdminPasswordChangeRevokesSessions proves changing a password ends the
// sessions issued under the old one. Without it, a leaked cookie survives the very
// action an operator takes to shut the leak down.
func TestAdminPasswordChangeRevokesSessions(t *testing.T) {
	ts, _ := sessionServer(t)
	session, csrf := loginCookies(t, ts)
	// A second sign-in, standing in for another browser the operator left open.
	other, _ := loginCookies(t, ts)

	resp := authedReq(t, ts, "PUT", "/admin/passwd", session, csrf,
		`{"old":"pw","new":"newpw"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("password change = %d, want 204", resp.StatusCode)
	}
	if code := whoamiWith(t, ts, session); code != http.StatusUnauthorized {
		t.Errorf("the changing session survived = %d, want 401", code)
	}
	if code := whoamiWith(t, ts, other); code != http.StatusUnauthorized {
		t.Errorf("another browser's session survived the password change = %d, want 401", code)
	}
}

// TestAdminSessionSurvivesWithoutARevocationStore is the control: a directory that
// keeps no sessions must keep working exactly as before, rather than locking every
// operator out because no row can be found.
func TestAdminSessionSurvivesWithoutARevocationStore(t *testing.T) {
	d := &fakeDir{authOK: true, password: "pw", uid: 7,
		roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	if code := whoamiWith(t, ts, session); code != http.StatusOK {
		t.Errorf("whoami without a session store = %d, want 200", code)
	}
}

// webmailSessionDir adds the webmail session table to the admin directory double,
// so a test can prove a password reset reaches the sessions that actually matter
// for a compromised mailbox user.
type webmailSessionDir struct {
	*sessionDir
	webmail map[string]string // jti -> email
}

func (d *webmailSessionDir) DeleteOtherWebmailSessions(email, keepJti string) (int64, error) {
	var n int64
	for jti, owner := range d.webmail {
		if strings.EqualFold(owner, email) && jti != keepJti {
			delete(d.webmail, jti)
			n++
		}
	}
	return n, nil
}

// TestPasswordResetEndsWebmailSessions is the incident-response case: an operator
// resets a compromised user's password. Ending only the panel's sessions leaves a
// stolen webmail cookie working for the rest of its lifetime, which looks like
// remediation while changing nothing for the attacker.
func TestPasswordResetEndsWebmailSessions(t *testing.T) {
	base := newSessionDir(&fakeDir{authOK: true, password: "pw", uid: 7,
		roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		knownUsers: map[string]directory.UserDetail{
			"victim@hermex.test": {Username: "victim@hermex.test", ID: 11},
		}})
	d := &webmailSessionDir{sessionDir: base, webmail: map[string]string{
		"stolen-jti": "victim@hermex.test",
		"other-jti":  "victim@hermex.test",
		"bystander":  "someone@hermex.test",
	}}
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	srv.store = &fakeStore{}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	session, csrf := loginCookies(t, ts)

	resp := authedReq(t, ts, "POST", "/admin/users/victim@hermex.test/password", session, csrf, `{"password":"newpass"}`)
	if s := statusOf(resp); s != http.StatusNoContent {
		t.Fatalf("password reset status = %d, want 204", s)
	}

	if _, ok := d.webmail["stolen-jti"]; ok {
		t.Error("the reset left the user's webmail session alive; a stolen cookie still works")
	}
	if _, ok := d.webmail["other-jti"]; ok {
		t.Error("the reset left a second webmail session alive")
	}
	if _, ok := d.webmail["bystander"]; !ok {
		t.Error("the reset revoked another account's session")
	}
}
