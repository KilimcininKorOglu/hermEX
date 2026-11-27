package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}

// TestRoutingByLongestPrefix proves requests reach the backend chosen by the
// longest case-insensitive prefix match, that the catch-all "/" is the default,
// and that the Authorization header is forwarded for the backend to authenticate.
func TestRoutingByLongestPrefix(t *testing.T) {
	echo := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := io.WriteString(w, name+" "+r.URL.Path+" auth="+r.Header.Get("Authorization")); err != nil {
				t.Errorf("write %s response: %v", name, err)
			}
		}))
	}
	mapi := echo("mapi")
	defer mapi.Close()
	ews := echo("ews")
	defer ews.Close()
	webmail := echo("webmail")
	defer webmail.Close()

	h, err := Handler([]Route{
		{Prefix: "/mapi/", Target: mapi.URL},
		{Prefix: "/rpc/", Target: mapi.URL},
		{Prefix: "/ews/", Target: ews.URL},
		{Prefix: "/autodiscover/", Target: ews.URL},
		{Prefix: "/", Target: webmail.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(h)
	defer front.Close()

	cases := []struct{ path, want string }{
		{"/mapi/emsmdb", "mapi"},
		{"/rpc/rpcproxy.dll", "mapi"},
		{"/EWS/Exchange.asmx", "ews"},             // upper-case path, lower-case prefix
		{"/Autodiscover/Autodiscover.xml", "ews"}, // Outlook desktop autodiscover
		{"/login", "webmail"},                     // catch-all default
		{"/", "webmail"},
	}
	for _, tc := range cases {
		req, err := http.NewRequest("GET", front.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Basic dGVzdA==")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		body, err := io.ReadAll(resp.Body)
		closeBody(t, resp)
		if err != nil {
			t.Fatalf("read GET %s: %v", tc.path, err)
		}
		got := string(body)
		if !strings.HasPrefix(got, tc.want+" ") {
			t.Errorf("GET %s routed to %q, want backend %s", tc.path, got, tc.want)
		}
		if !strings.Contains(got, "auth=Basic dGVzdA==") {
			t.Errorf("GET %s did not forward Authorization: %q", tc.path, got)
		}
	}
}

// TestHandlerErrors proves construction rejects an empty route set and any
// malformed or non-absolute target URL without returning a partial handler.
func TestHandlerErrors(t *testing.T) {
	cases := []struct {
		name   string
		routes []Route
	}{
		{"nil-routes", nil},
		{"empty-routes", []Route{}},
		{"non-absolute-target", []Route{{Prefix: "/", Target: "not-a-url"}}},
		{"unparseable-url", []Route{{Prefix: "/", Target: "http://[::1"}}},
		{"missing-host", []Route{{Prefix: "/", Target: "http://"}}},
		{"missing-scheme", []Route{{Prefix: "/", Target: "//backend:8080"}}},
		{"invalid-second-route", []Route{
			{Prefix: "/ews/", Target: "http://127.0.0.1:8080"},
			{Prefix: "/bad/", Target: "/relative/only"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, err := Handler(tc.routes)
			if err == nil {
				t.Errorf("%s: expected error, got nil", tc.name)
			}
			if h != nil {
				t.Errorf("%s: expected nil handler on error, got non-nil", tc.name)
			}
		})
	}
}

// TestUnroutedRequestsDoNotReachBackends proves hostile or unmatched paths
// receive 502 and never touch any configured backend when no catch-all exists.
func TestUnroutedRequestsDoNotReachBackends(t *testing.T) {
	var ewsHits, mapiHits atomic.Int64
	makeBackend := func(hits *atomic.Int64) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			if _, err := io.WriteString(w, "ok"); err != nil {
				t.Errorf("write backend response: %v", err)
			}
		}))
	}
	ews := makeBackend(&ewsHits)
	defer ews.Close()
	mapi := makeBackend(&mapiHits)
	defer mapi.Close()

	h, err := Handler([]Route{
		{Prefix: "/ews/", Target: ews.URL},
		{Prefix: "/mapi/", Target: mapi.URL},
	})
	if err != nil {
		t.Fatal(err)
	}

	badPaths := []string{
		"/",
		"/admin",
		"/ews",
		"/ewsx/Exchange.asmx",
		"/" + strings.Repeat("x", 4096),
	}
	for _, p := range badPaths {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusBadGateway {
			t.Errorf("GET %q: status = %d, want 502", p, rec.Code)
		}
		if ewsHits.Load() != 0 || mapiHits.Load() != 0 {
			t.Errorf("GET %q reached backend (ews=%d, mapi=%d), want 0", p, ewsHits.Load(), mapiHits.Load())
		}
	}

	// Follow-up valid request confirms routing still works after refusals.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ews/Exchange.asmx", nil))
	if rec.Code != http.StatusOK || ewsHits.Load() != 1 || mapiHits.Load() != 0 {
		t.Errorf("valid GET /ews/Exchange.asmx: status=%d ews=%d mapi=%d, want 200/1/0", rec.Code, ewsHits.Load(), mapiHits.Load())
	}
}

// TestForwardedForNamesTheClient proves the proxy tells the backend which client it is
// serving. The backends key their rate limiter and their access log on the first
// X-Forwarded-For hop, so this header must carry the client the gateway actually
// accepted. The front door strips whatever the client sent before this runs
// (serve.FrontDoor), which is what makes the value here trustworthy.
func TestForwardedForNamesTheClient(t *testing.T) {
	var got string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Forwarded-For")
	}))
	defer backend.Close()

	h, err := Handler([]Route{{Prefix: "/", Target: backend.URL}})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(h)
	defer front.Close()

	resp, err := http.Get(front.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	closeBody(t, resp)
	if got == "" {
		t.Fatal("backend received no X-Forwarded-For, so it cannot tell one client from another")
	}
	// The test client dials the front door over loopback, so that is the address
	// the gateway accepted and the only value the backend may be told.
	if got != "127.0.0.1" {
		t.Errorf("X-Forwarded-For = %q, want the accepted client 127.0.0.1 as the single hop", got)
	}
}
