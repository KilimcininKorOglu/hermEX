package webmail2api

import (
	"net/http"
	"net/http/httptest"
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

	// An unauthenticated API call still returns headers on the response.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/favorites", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	want := map[string]string{
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}
