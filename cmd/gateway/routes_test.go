package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRouteHandlerSendsTLSReportsToTheMTA pins the front door's route for TLS
// reports posted over HTTPS: /tlsrpt reaches the MTA, and everything the route
// table does not name still falls through to webmail.
func TestRouteHandlerSendsTLSReportsToTheMTA(t *testing.T) {
	backend := func(name string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, name) // a failed write fails the assertion below
		}))
		t.Cleanup(s.Close)
		return s
	}
	web, mta := backend("webmail"), backend("mta")
	h := routeHandler(gatewaySettings{
		backendMapi: web.URL, backendEws: web.URL, backendActiveSync: web.URL,
		backendDav: web.URL, backendWebmail: web.URL, backendMta: mta.URL,
	})

	for path, want := range map[string]string{"/tlsrpt": "mta", "/tlsrpt/": "mta", "/inbox": "webmail"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, nil))
		if got := rr.Body.String(); got != want {
			t.Errorf("%s reached %q, want %q", path, got, want)
		}
	}
}
