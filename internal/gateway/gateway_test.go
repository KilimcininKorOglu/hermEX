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
	resp.Body.Close()
	if got == "" {
		t.Fatal("backend received no X-Forwarded-For, so it cannot tell one client from another")
	}
	if strings.Contains(got, ",") {
		t.Errorf("X-Forwarded-For = %q, want a single hop: the front door strips the client's own", got)
	}
}
