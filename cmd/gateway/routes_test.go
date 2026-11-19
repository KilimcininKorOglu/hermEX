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

// TestAdminPanelOnlyUnderItsHostName: the admin panel is published under its
// configured host name and nowhere else. Every path of that name reaches the
// panel; the panel's own paths under a mail host reach webmail, never the panel;
// a client-sent X-Forwarded-Host cannot steer a request there; and with no host
// name configured no request reaches it.
func TestAdminPanelOnlyUnderItsHostName(t *testing.T) {
	backend := func(name string) string {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, name)
		}))
		t.Cleanup(s.Close)
		return s.URL
	}
	gw := gatewaySettings{
		backendMapi: backend("mapi"), backendEws: backend("ews"), backendActiveSync: backend("activesync"),
		backendDav: backend("dav"), backendWebmail: backend("webmail"), backendMta: backend("mta"),
		backendAdmin: backend("admin"), adminHost: "admin.example.com",
	}
	h := routeHandler(gw)
	reach := func(host, path string, header map[string]string) string {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = host
		for k, v := range header {
			r.Header.Set(k, v)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		return rr.Body.String()
	}
	for _, c := range []struct{ host, path, want string }{
		{"admin.example.com", "/admin/ui/", "admin"},
		{"admin.example.com", "/", "admin"},
		{"ADMIN.Example.COM:443", "/ews/exchange.asmx", "admin"},
		{"admin.example.com.", "/admin/ui/login", "admin"},
		{"mail.example.com", "/admin/ui/", "webmail"},
		{"admin.example.com.evil.test", "/admin/ui/", "webmail"},
		{"xadmin.example.com", "/admin/ui/", "webmail"},
	} {
		if got := reach(c.host, c.path, nil); got != c.want {
			t.Errorf("Host %q %s reached %q, want %q", c.host, c.path, got, c.want)
		}
	}
	if got := reach("mail.example.com", "/admin/ui/", map[string]string{"X-Forwarded-Host": "admin.example.com"}); got != "webmail" {
		t.Errorf("a forged X-Forwarded-Host reached %q, want webmail", got)
	}
	gw.adminHost = ""
	h = routeHandler(gw)
	if got := reach("admin.example.com", "/admin/ui/", nil); got != "webmail" {
		t.Errorf("with no admin host name the panel was reached (%q)", got)
	}
}
