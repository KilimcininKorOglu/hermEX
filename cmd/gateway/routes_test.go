package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRouteHandlerReachesEachBackend pins the front door's route table against
// the paths real clients send. Each backend answers with its own name, so a
// request routed to the wrong daemon is visible. The paths are the wire paths:
// Outlook and Exchange clients spell /EWS/, /Autodiscover/ and
// /Microsoft-Server-ActiveSync in mixed case, MAPI/HTTP uses /mapi/emsmdb/,
// RPC over HTTP uses /rpc/rpcproxy.dll, CalDAV/CardDAV discovery uses the
// RFC 6764 well-known paths, and TLS reports arrive at /tlsrpt (RFC 8460 §3).
// Everything the table does not name falls through to webmail.
func TestRouteHandlerReachesEachBackend(t *testing.T) {
	backend := func(name string) string {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, name) // a failed write fails the assertion below
		}))
		t.Cleanup(s.Close)
		return s.URL
	}
	h := routeHandler(gatewaySettings{
		backendMapi: backend("mapi"), backendEws: backend("ews"), backendActiveSync: backend("activesync"),
		backendDav: backend("dav"), backendWebmail: backend("webmail"), backendMta: backend("mta"),
	})

	for path, want := range map[string]string{
		"/mapi/emsmdb/?MailboxId=x":                  "mapi",
		"/rpc/rpcproxy.dll":                          "mapi",
		"/rpcwithcert/rpcproxy.dll":                  "mapi",
		"/EWS/Exchange.asmx":                         "ews",
		"/Autodiscover/Autodiscover.xml":             "ews",
		"/Microsoft-Server-ActiveSync?Cmd=Sync":      "activesync",
		"/.well-known/caldav":                        "dav",
		"/.well-known/carddav":                       "dav",
		"/dav/calendars/alice@hermex.test/Calendar/": "dav",
		"/tlsrpt":       "mta",
		"/tlsrpt/":      "mta",
		"/inbox":        "webmail",
		"/":             "webmail",
		"/ewsx/not-ews": "webmail",
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, nil))
		if got := rr.Body.String(); got != want {
			t.Errorf("%s reached %q, want %q", path, got, want)
		}
	}
}
