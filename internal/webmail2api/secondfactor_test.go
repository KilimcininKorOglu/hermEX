package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/totp"
)

// sfAuth is a directory that authenticates one account and holds its
// second-factor enrollment in memory, which is what lets these tests drive the
// real handlers without a database.
type sfAuth struct {
	accounts directory.StaticAccounts
	secret   string
	enabled  bool
	lastStep int64
	codes    []string // unspent recovery-code hashes
}

func (a *sfAuth) Authenticate(user, password string) (string, bool) {
	return a.accounts.Authenticate(user, password)
}

func (a *sfAuth) TOTPEnrollment(string) (directory.TOTPEnrollment, bool, error) {
	if a.secret == "" {
		return directory.TOTPEnrollment{}, false, nil
	}
	return directory.TOTPEnrollment{Secret: a.secret, Enabled: a.enabled}, true, nil
}

func (a *sfAuth) BeginTOTPEnrollment(_, secret string) error {
	if a.enabled {
		return directory.ErrTOTPAlreadyEnabled
	}
	a.secret = secret
	return nil
}

func (a *sfAuth) ActivateTOTP(_ string, step int64, hashes []string) error {
	a.enabled, a.lastStep, a.codes = true, step, hashes
	return nil
}

func (a *sfAuth) DisableTOTP(string) error {
	a.secret, a.enabled, a.lastStep, a.codes = "", false, 0, nil
	return nil
}

func (a *sfAuth) ConsumeTOTPStep(_ string, step int64) (bool, error) {
	if !a.enabled || step <= a.lastStep {
		return false, nil
	}
	a.lastStep = step
	return true, nil
}

func (a *sfAuth) ConsumeRecoveryCode(_, code string) (bool, error) {
	i := totp.MatchRecoveryCode(a.codes, code)
	if i < 0 {
		return false, nil
	}
	a.codes = append(a.codes[:i], a.codes[i+1:]...)
	return true, nil
}

func (a *sfAuth) RecoveryCodesRemaining(string) (int, error) { return len(a.codes), nil }

// sfHarness seeds a mailbox and returns the directory plus one browser.
func sfHarness(t *testing.T) (*sfAuth, requestFunc) {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	auth := &sfAuth{accounts: directory.StaticAccounts{
		"alice@hermex.test": {Password: "pw", MailboxPath: dir},
	}}
	return auth, browser(auth)
}

// requestFunc is one browser: it carries its own cookie jar across calls.
type requestFunc func(method, target, body string) *httptest.ResponseRecorder

// browser returns a second, independent client against the same directory, which
// is what a replay test needs: the attacker is not the user's own session.
func browser(auth *sfAuth) requestFunc {
	srv := NewServer(auth, auth.accounts, nil, "mail.hermex.test", []byte("second-factor-secret"), "", false)
	var jar []*http.Cookie
	return func(method, target, body string) *httptest.ResponseRecorder {
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
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if set := rec.Result().Cookies(); len(set) > 0 {
			jar = set
		}
		return rec
	}
}

// login signs in and returns the decoded answer.
func sfLogin(t *testing.T, do requestFunc) map[string]any {
	t.Helper()
	rec := do(http.MethodPost, "/api/v1/auth/login", `{"email":"alice@hermex.test","password":"pw"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// enroll takes one account all the way through setup and returns its recovery
// codes.
func sfEnroll(t *testing.T, auth *sfAuth, do requestFunc) []string {
	t.Helper()
	sfLogin(t, do)
	if rec := do(http.MethodPost, "/api/v1/account/2fa/begin", ""); rec.Code != http.StatusOK {
		t.Fatalf("begin = %d: %s", rec.Code, rec.Body.String())
	}
	code, err := totp.Code(auth.secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec := do(http.MethodPost, "/api/v1/account/2fa/activate", `{"code":"`+code+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("activate = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.RecoveryCodes) != totp.RecoveryCodeCount {
		t.Fatalf("activation returned %d recovery codes", len(out.RecoveryCodes))
	}
	return out.RecoveryCodes
}

// sfNextCode returns a code from the step AFTER the current one, still inside
// the accepted skew. Enrollment spends the step of the code that proved it, so a
// test signing in straight afterwards has to present the next one, exactly as a
// user waiting half a minute would.
func sfNextCode(t *testing.T, auth *sfAuth) string {
	t.Helper()
	code, err := totp.Code(auth.secret, time.Now().Add(totp.Step))
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// TestAnUnenrolledLoginIsUnchanged keeps the second factor invisible to every
// account that has not asked for one.
func TestAnUnenrolledLoginIsUnchanged(t *testing.T) {
	_, do := sfHarness(t)
	out := sfLogin(t, do)
	if out["secondFactorRequired"] != nil {
		t.Errorf("an unenrolled login was told a second factor is required: %v", out)
	}
	if rec := do(http.MethodGet, "/api/v1/folders", ""); rec.Code == http.StatusForbidden {
		t.Error("an unenrolled session was gated")
	}
}

// TestAPendingSessionReachesNothing is the whole point of the gate. The pending
// cookie is a real session cookie, so without it the code prompt would be a
// screen the SPA draws and the API would serve the mailbox anyway.
func TestAPendingSessionReachesNothing(t *testing.T) {
	auth, do := sfHarness(t)
	sfEnroll(t, auth, do)

	out := sfLogin(t, do)
	if out["secondFactorRequired"] != true {
		t.Fatalf("an enrolled login was not told to produce a code: %v", out)
	}
	for _, p := range []string{"/api/v1/folders", "/api/v1/mail/Inbox", "/api/v1/settings/appearance", "/api/v1/account/2fa"} {
		if rec := do(http.MethodGet, p, ""); rec.Code != http.StatusForbidden {
			t.Errorf("%s with a pending session = %d, want 403", p, rec.Code)
		}
	}
	// The password-change endpoint is barred too: a half-finished login must not
	// be able to set a new password and lock the real owner out.
	if rec := do(http.MethodPost, "/api/v1/account/password", `{"current":"pw","new":"x"}`); rec.Code != http.StatusForbidden {
		t.Errorf("the password change with a pending session = %d, want 403", rec.Code)
	}
	// And it says so on the probe the SPA reads, without describing the mailbox.
	rec := do(http.MethodGet, "/api/v1/auth/me", "")
	var me map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me["second_factor_required"] != true {
		t.Errorf("auth/me did not report the pending state: %v", me)
	}
	if _, leaked := me["onboarded"]; leaked {
		t.Errorf("auth/me described the mailbox to a pending session: %v", me)
	}
}

// TestTheCodeCompletesTheLogin is the other half: a correct code turns the
// pending session into a real one.
func TestTheCodeCompletesTheLogin(t *testing.T) {
	auth, do := sfHarness(t)
	sfEnroll(t, auth, do)
	sfLogin(t, do)

	code := sfNextCode(t, auth)
	if rec := do(http.MethodPost, "/api/v1/auth/2fa/verify", `{"code":"`+code+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("verify = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodGet, "/api/v1/account/2fa", ""); rec.Code != http.StatusOK {
		t.Errorf("the completed session is still gated: %d", rec.Code)
	}
}

// TestAWrongCodeLeavesTheSessionPending covers the failure path, including the
// one that matters most: the session must not be upgraded by a refused code.
func TestAWrongCodeLeavesTheSessionPending(t *testing.T) {
	auth, do := sfHarness(t)
	sfEnroll(t, auth, do)
	sfLogin(t, do)

	if rec := do(http.MethodPost, "/api/v1/auth/2fa/verify", `{"code":"000000"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong code = %d, want 401", rec.Code)
	}
	if rec := do(http.MethodGet, "/api/v1/folders", ""); rec.Code != http.StatusForbidden {
		t.Errorf("the session was upgraded by a refused code: %d", rec.Code)
	}
}

// TestACodeIsNotReplayable is the replay guard as the login path sees it. A code
// is valid for its whole step, so an observer has the rest of that window to
// present the same digits.
func TestACodeIsNotReplayable(t *testing.T) {
	auth, do := sfHarness(t)
	sfEnroll(t, auth, do)
	sfLogin(t, do)

	code := sfNextCode(t, auth)
	if rec := do(http.MethodPost, "/api/v1/auth/2fa/verify", `{"code":"`+code+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("verify = %d: %s", rec.Code, rec.Body.String())
	}
	// A second browser, with the same digits, inside the same step.
	other := browser(auth)
	sfLogin(t, other)
	if rec := other(http.MethodPost, "/api/v1/auth/2fa/verify", `{"code":"`+code+`"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("the same code was accepted twice: %d", rec.Code)
	}
}

// TestTheEnrollmentCodeIsNotAlsoTheFirstLogin covers the seam between setup and
// the next sign-in: the code that proved the enrollment is spent by it.
func TestTheEnrollmentCodeIsNotAlsoTheFirstLogin(t *testing.T) {
	auth, do := sfHarness(t)
	code, err := func() (string, error) {
		sfEnroll(t, auth, do)
		return totp.Code(auth.secret, time.Now())
	}()
	if err != nil {
		t.Fatal(err)
	}
	sfLogin(t, do)
	if rec := do(http.MethodPost, "/api/v1/auth/2fa/verify", `{"code":"`+code+`"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("the enrollment code signed the user in: %d", rec.Code)
	}
}

// TestARecoveryCodeSignsInOnce is the way back when the authenticator is gone,
// and it has to stop working the moment it is used.
func TestARecoveryCodeSignsInOnce(t *testing.T) {
	auth, do := sfHarness(t)
	codes := sfEnroll(t, auth, do)
	sfLogin(t, do)

	if rec := do(http.MethodPost, "/api/v1/auth/2fa/verify", `{"code":"`+codes[0]+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("a recovery code was refused: %d %s", rec.Code, rec.Body.String())
	}
	other := browser(auth)
	sfLogin(t, other)
	if rec := other(http.MethodPost, "/api/v1/auth/2fa/verify", `{"code":"`+codes[0]+`"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("the same recovery code was accepted twice: %d", rec.Code)
	}
}

// TestDisablingAsksForThePassword keeps a stolen session from simply turning the
// second factor off, which is what its holder would do first.
func TestDisablingAsksForThePassword(t *testing.T) {
	auth, do := sfHarness(t)
	sfEnroll(t, auth, do)
	sfLogin(t, do)
	code := sfNextCode(t, auth)
	if rec := do(http.MethodPost, "/api/v1/auth/2fa/verify", `{"code":"`+code+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("verify = %d", rec.Code)
	}

	if rec := do(http.MethodPost, "/api/v1/account/2fa/disable", `{"password":"wrong"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("disable with a wrong password = %d, want 401", rec.Code)
	}
	if !auth.enabled {
		t.Fatal("the second factor was removed by a wrong password")
	}
	if rec := do(http.MethodPost, "/api/v1/account/2fa/disable", `{"password":"pw"}`); rec.Code != http.StatusOK {
		t.Fatalf("disable = %d: %s", rec.Code, rec.Body.String())
	}
	if auth.enabled {
		t.Error("the second factor survived a correct disable")
	}
}

// TestAnActiveEnrollmentIsNotRestarted keeps the enrollment endpoint from being
// the bypass: a live session could otherwise mint a new secret of its own.
func TestAnActiveEnrollmentIsNotRestarted(t *testing.T) {
	auth, do := sfHarness(t)
	sfEnroll(t, auth, do)
	sfLogin(t, do)
	code := sfNextCode(t, auth)
	if rec := do(http.MethodPost, "/api/v1/auth/2fa/verify", `{"code":"`+code+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("verify = %d", rec.Code)
	}
	before := auth.secret
	if rec := do(http.MethodPost, "/api/v1/account/2fa/begin", ""); rec.Code != http.StatusConflict {
		t.Errorf("begin over an active enrollment = %d, want 409", rec.Code)
	}
	if auth.secret != before {
		t.Error("the active secret was replaced")
	}
}
