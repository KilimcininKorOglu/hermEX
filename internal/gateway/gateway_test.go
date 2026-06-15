package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRoutingByLongestPrefix proves requests reach the backend chosen by the
// longest case-insensitive prefix match, that the catch-all "/" is the default,
// and that the Authorization header is forwarded for the backend to authenticate.
func TestRoutingByLongestPrefix(t *testing.T) {
	echo := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, name+" "+r.URL.Path+" auth="+r.Header.Get("Authorization"))
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
		req, _ := http.NewRequest("GET", front.URL+tc.path, nil)
		req.Header.Set("Authorization", "Basic dGVzdA==")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		got := string(body)
		if !strings.HasPrefix(got, tc.want+" ") {
			t.Errorf("GET %s routed to %q, want backend %s", tc.path, got, tc.want)
		}
		if !strings.Contains(got, "auth=Basic dGVzdA==") {
			t.Errorf("GET %s did not forward Authorization: %q", tc.path, got)
		}
	}
}

// TestHandlerErrors proves construction rejects an empty route set and a target
// that is not an absolute URL.
func TestHandlerErrors(t *testing.T) {
	if _, err := Handler(nil); err == nil {
		t.Error("empty routes should error")
	}
	if _, err := Handler([]Route{{Prefix: "/", Target: "not-a-url"}}); err == nil {
		t.Error("non-absolute target should error")
	}
}
