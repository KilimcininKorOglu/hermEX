package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestSecurityHeaders proves every response carries the clickjacking and
// MIME-sniffing defences, so the webmail can never be framed by an attacker's
// page nor have a declared Content-Type second-guessed by the browser. The
// headers are stamped by the outermost middleware, so they apply uniformly to
// API JSON, the SPA, and unauthenticated routes alike.
func TestSecurityHeaders(t *testing.T) {
	srv := NewServer(flaggedAuth{mbox: t.TempDir()}, directory.StaticAccounts{}, nil, "mail.hermex.test", []byte("sec-hdr-secret"), "", false)

	want := map[string]string{
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
	}
	// An unauthenticated API call, an unknown API route, a refused login with
	// a malformed body, and the SPA path all carry the headers.
	for _, c := range []struct{ method, target, body string }{
		{http.MethodGet, "/api/v1/favorites", ""},
		{http.MethodGet, "/api/v1/no-such-route", ""},
		{http.MethodPost, "/api/v1/auth/login", "{"},
		{http.MethodGet, "/inbox", ""},
	} {
		req := httptest.NewRequest(c.method, c.target, strings.NewReader(c.body))
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		for k, v := range want {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s %s: %s = %q, want %q", c.method, c.target, k, got, v)
			}
		}
	}
}
