package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The redirect listener must send every plain-HTTP request to the same host,
// path and query over https (RFC 9110 15.4.2, 301), dropping the plain port.
func TestHTTPSRedirectKeepsHostPathAndQuery(t *testing.T) {
	cases := []struct{ host, target, want string }{
		{"mail.example.com", "/owa/?a=1&b=2", "https://mail.example.com/owa/?a=1&b=2"},
		{"mail.example.com:80", "/", "https://mail.example.com/"},
		{"Admin.Example.com", "/admin/ui/login", "https://Admin.Example.com/admin/ui/login"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.target, nil)
		req.Host = c.host
		rec := httptest.NewRecorder()
		httpsRedirect().ServeHTTP(rec, req)
		if rec.Code != http.StatusMovedPermanently {
			t.Fatalf("%s%s: status %d, want 301", c.host, c.target, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != c.want {
			t.Fatalf("%s%s: Location %q, want %q", c.host, c.target, got, c.want)
		}
	}
}

// A hostile Host header must not shape the Location: anything that is not a
// plain host name is refused instead of redirected.
func TestHTTPSRedirectRefusesHostileHost(t *testing.T) {
	for _, host := range []string{"", "evil.example/@good", "a b", "evil.example\\x", "x\r\nSet-Cookie:a=b", string(make([]byte, 300))} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		httpsRedirect().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("host %q: status %d, want 400", host, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "" {
			t.Fatalf("host %q: Location %q set", host, loc)
		}
	}
}
