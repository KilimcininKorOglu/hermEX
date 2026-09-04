package mapihttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSessionCookieAttributes pins the attributes of both MAPI/HTTP session
// cookies. The endpoints answer MAPI clients, which ignore every attribute here,
// so nothing in the protocol path enforces them: only this test does.
func TestSessionCookieAttributes(t *testing.T) {
	cases := []struct {
		name string
		set  func(http.ResponseWriter, string, string)
		path string
	}{
		{"emsmdb", setCookie, "/mapi/emsmdb"},
		{"nspi", setNspiCookie, "/mapi/nspi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.set(rec, "sessionid", "abc")
			cookies := (&http.Response{Header: rec.Header()}).Cookies()
			if len(cookies) != 1 {
				t.Fatalf("got %d cookies, want 1", len(cookies))
			}
			c := cookies[0]
			if c.Name != "sessionid" || c.Value != "abc" {
				t.Errorf("cookie = %s=%s", c.Name, c.Value)
			}
			if c.Path != tc.path {
				t.Errorf("Path = %q, want %q", c.Path, tc.path)
			}
			if !c.HttpOnly {
				t.Error("HttpOnly is not set")
			}
			if !c.Secure {
				t.Error("Secure is not set")
			}
			if c.SameSite != http.SameSiteStrictMode {
				t.Errorf("SameSite = %v, want Strict", c.SameSite)
			}
		})
	}
}
