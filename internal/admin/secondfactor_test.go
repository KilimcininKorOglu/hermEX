package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/totp"
)

// sfDir is a fakeDir that also carries a second-factor enrollment, which is what
// the optional capability is reached through.
type sfDir struct {
	*fakeDir
	secret   string
	enabled  bool
	lastStep int64
	codes    []string
}

func (d *sfDir) TOTPEnrollment(string) (directory.TOTPEnrollment, bool, error) {
	if d.secret == "" {
		return directory.TOTPEnrollment{}, false, nil
	}
	return directory.TOTPEnrollment{Secret: d.secret, Enabled: d.enabled}, true, nil
}

func (d *sfDir) BeginTOTPEnrollment(_, secret string) error {
	d.secret = secret
	return nil
}

func (d *sfDir) ActivateTOTP(_ string, step int64, hashes []string) error {
	d.enabled, d.lastStep, d.codes = true, step, hashes
	return nil
}

func (d *sfDir) DisableTOTP(string) error {
	d.secret, d.enabled, d.codes = "", false, nil
	return nil
}

func (d *sfDir) ConsumeTOTPStep(_ string, step int64) (bool, error) {
	if !d.enabled || step <= d.lastStep {
		return false, nil
	}
	d.lastStep = step
	return true, nil
}

func (d *sfDir) ConsumeRecoveryCode(_, code string) (bool, error) {
	i := totp.MatchRecoveryCode(d.codes, code)
	if i < 0 {
		return false, nil
	}
	d.codes = append(d.codes[:i], d.codes[i+1:]...)
	return true, nil
}

func (d *sfDir) RecoveryCodesRemaining(string) (int, error) { return len(d.codes), nil }

// enrolledAdminServer returns a panel whose one administrator carries a second
// factor, plus a client that keeps its cookies.
func enrolledAdminServer(t *testing.T) (*sfDir, *httptest.Server, *http.Client) {
	t.Helper()
	secret, err := totp.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	d := &sfDir{
		fakeDir: &fakeDir{authOK: true, password: "correct", uid: 7,
			roles: []directory.AdminRole{{Role: directory.AdminSystem}},
			perms: []directory.Permission{{Name: directory.PermSystemAdmin}}},
		secret: secret, enabled: true,
	}
	ts := adminServer(t, d)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Redirects are not followed: the tests assert on the redirect itself, which
	// is what says where a half-finished login was sent.
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return d, ts, client
}

// csrfFor returns the CSRF cookie the client currently holds. The panel scopes
// its cookies to /admin, so the jar is asked for that path rather than the root,
// which matches nothing.
func csrfFor(t *testing.T, client *http.Client, ts *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(ts.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range client.Jar.Cookies(u) {
		if c.Name == csrfCookie {
			return c.Value
		}
	}
	return ""
}

// apiLogin signs in through the JSON endpoint and returns the decoded answer.
func apiLogin(t *testing.T, client *http.Client, ts *httptest.Server) map[string]any {
	t.Helper()
	resp, err := client.Post(ts.URL+"/admin/login", "application/json",
		strings.NewReader(`{"login":"admin@hermex.test","password":"correct"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d: %s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// verifyCode submits a code to the JSON endpoint.
func verifyCode(t *testing.T, client *http.Client, ts *httptest.Server, code string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/admin/2fa/verify",
		strings.NewReader(`{"code":"`+code+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrfHeader, csrfFor(t, client, ts))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// get performs an authenticated GET and returns its status.
func getStatus(t *testing.T, client *http.Client, ts *httptest.Server, path string) int {
	t.Helper()
	resp, err := client.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// nextCode returns a code from the step after the current one, which is what a
// second sign-in needs once the previous step has been spent.
func nextCode(t *testing.T, d *sfDir) string {
	t.Helper()
	code, err := totp.Code(d.secret, time.Now().Add(totp.Step))
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// TestAPendingPanelSessionReachesNothing is the gate. The panel is the
// highest-privilege surface in the system, so a session that has cleared only
// the password must not reach a single page or endpoint of it.
func TestAPendingPanelSessionReachesNothing(t *testing.T) {
	_, ts, client := enrolledAdminServer(t)
	out := apiLogin(t, client, ts)
	if out["secondFactorRequired"] != true {
		t.Fatalf("an enrolled operator was signed straight in: %v", out)
	}
	if _, listed := out["roles"]; listed {
		t.Errorf("the pending answer described the operator's roles: %v", out)
	}
	if code := getStatus(t, client, ts, "/admin/whoami"); code != http.StatusForbidden {
		t.Errorf("whoami with a pending session = %d, want 403", code)
	}
	// The panel pages read a pending cookie as no session at all, so they send
	// the operator back to the sign-in form rather than rendering.
	if code := getStatus(t, client, ts, "/admin/ui/"); code != http.StatusSeeOther {
		t.Errorf("the dashboard with a pending session = %d, want a redirect", code)
	}
}

// TestTheCodeCompletesThePanelLogin is the other half.
func TestTheCodeCompletesThePanelLogin(t *testing.T) {
	d, ts, client := enrolledAdminServer(t)
	apiLogin(t, client, ts)

	if code := verifyCode(t, client, ts, "000000"); code != http.StatusUnauthorized {
		t.Fatalf("a wrong code = %d, want 401", code)
	}
	if code := getStatus(t, client, ts, "/admin/whoami"); code != http.StatusForbidden {
		t.Fatalf("the session was upgraded by a refused code: %d", code)
	}
	if code := verifyCode(t, client, ts, nextCode(t, d)); code != http.StatusOK {
		t.Fatalf("a correct code = %d, want 200", code)
	}
	if code := getStatus(t, client, ts, "/admin/whoami"); code != http.StatusOK {
		t.Errorf("the completed session is still gated: %d", code)
	}
}

// TestAPanelCodeIsNotReplayable covers the replay window on the panel, where the
// consequence of one observed code is the whole deployment.
func TestAPanelCodeIsNotReplayable(t *testing.T) {
	d, ts, client := enrolledAdminServer(t)
	apiLogin(t, client, ts)
	code := nextCode(t, d)
	if got := verifyCode(t, client, ts, code); got != http.StatusOK {
		t.Fatalf("verify = %d", got)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	other := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	apiLogin(t, other, ts)
	if got := verifyCode(t, other, ts, code); got != http.StatusUnauthorized {
		t.Errorf("the same code was accepted twice: %d", got)
	}
}

// TestThePanelFormLoginStopsAtTheCodePrompt covers the htmx path, which is the
// one an operator actually uses.
func TestThePanelFormLoginStopsAtTheCodePrompt(t *testing.T) {
	d, ts, client := enrolledAdminServer(t)
	form := url.Values{"login": {"admin@hermex.test"}, "password": {"correct"}}
	resp, err := client.Post(ts.URL+"/admin/ui/login", "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Location"); got != "/admin/ui/second-factor" {
		t.Fatalf("the form login went to %q, want the code prompt", got)
	}
	if code := getStatus(t, client, ts, "/admin/ui/second-factor"); code != http.StatusOK {
		t.Fatalf("the code prompt = %d", code)
	}

	codeForm := url.Values{"code": {nextCode(t, d)}, "_csrf": {csrfFor(t, client, ts)}}
	resp, err = client.Post(ts.URL+"/admin/ui/second-factor", "application/x-www-form-urlencoded",
		strings.NewReader(codeForm.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Location"); got != "/admin/ui/" {
		t.Fatalf("a correct code went to %q, want the dashboard", got)
	}
	if code := getStatus(t, client, ts, "/admin/whoami"); code != http.StatusOK {
		t.Errorf("the completed panel session is still gated: %d", code)
	}
}

// TestAnUnenrolledOperatorIsUnchanged keeps the panel exactly as it was for every
// account that has not asked for a second factor, including one whose directory
// has no such capability at all.
func TestAnUnenrolledOperatorIsUnchanged(t *testing.T) {
	for _, c := range []struct {
		name string
		dir  Directory
	}{
		{"no enrollment", &sfDir{fakeDir: &fakeDir{authOK: true, password: "correct", uid: 7,
			roles: []directory.AdminRole{{Role: directory.AdminSystem}},
			perms: []directory.Permission{{Name: directory.PermSystemAdmin}}}}},
		{"no capability", &fakeDir{authOK: true, password: "correct", uid: 7,
			roles: []directory.AdminRole{{Role: directory.AdminSystem}},
			perms: []directory.Permission{{Name: directory.PermSystemAdmin}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ts := adminServer(t, c.dir)
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			}}
			out := apiLogin(t, client, ts)
			if out["secondFactorRequired"] != nil {
				t.Errorf("an unenrolled operator was asked for a code: %v", out)
			}
			if code := getStatus(t, client, ts, "/admin/whoami"); code != http.StatusOK {
				t.Errorf("an unenrolled session was gated: %d", code)
			}
		})
	}
}
