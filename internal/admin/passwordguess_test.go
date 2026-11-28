package admin

import (
	"net/http"
	"net/url"
	"testing"

	"hermex/internal/authlimit"
)

// TestChangeOwnPasswordThrottlesGuesses proves both of the panel's own-password
// changes check the current password under the sign-in throttle. A stolen session
// could otherwise guess the operator's password there without limit, a path the
// login throttle never sees. After the failure threshold even the right password
// is refused with 429, as at sign-in, and the password is not changed.
func TestChangeOwnPasswordThrottlesGuesses(t *testing.T) {
	t.Run("panel form", func(t *testing.T) {
		d := cpFakeDir()
		ts := adminServer(t, d)
		session, csrf := loginCookies(t, ts)
		form := func(old string) url.Values { return url.Values{"old": {old}, "new": {"newsecret"}} }
		wantStatus(t, htmxPUT(t, ts, "/admin/ui/change-password", "", csrf, form("pw")),
			http.StatusUnauthorized, "unauthenticated panel change-password")
		wantEq(t, d.setPwValue, "", "the password after unauth attempt")
		for range authlimit.DefaultMaxFails {
			wantStatus(t, htmxPUT(t, ts, "/admin/ui/change-password", session, csrf, form("guess")),
				http.StatusOK, "wrong current password guess")
		}
		wantStatus(t, htmxPUT(t, ts, "/admin/ui/change-password", session, csrf, form("pw")),
			http.StatusTooManyRequests, "the right password after the guesses")
		wantEq(t, d.setPwValue, "", "the password during the lockout")
	})
	t.Run("JSON API", func(t *testing.T) {
		d := cpFakeDir()
		ts := adminServer(t, d)
		session, csrf := loginCookies(t, ts)
		for i := range authlimit.DefaultMaxFails {
			resp := authedReq(t, ts, "PUT", "/admin/passwd", session, csrf, `{"old":"guess","new":"newsecret"}`)
			if s := statusOf(resp); s != http.StatusForbidden {
				t.Fatalf("guess %d = %d, want 403 (wrong current password)", i, s)
			}
		}
		wantStatus(t, authedReq(t, ts, "PUT", "/admin/passwd", session, csrf, `{"old":"pw","new":"newsecret"}`),
			http.StatusTooManyRequests, "the right password after the guesses")
		wantEq(t, d.setPwValue, "", "the password during the lockout")
	})
}
