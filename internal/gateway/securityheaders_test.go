package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBackendSecurityHeadersAreNotDuplicated proves the front door leaves exactly
// one copy of each stamped header. The backend stamps its own (every daemon does,
// they share one listener base) and the proxy copies backend headers on top of
// whatever the front door already set, so both would reach the client. A header
// delivered twice is invalid, and a browser may ignore it outright, which turns
// two layers of the same defense into none.
func TestBackendSecurityHeadersAreNotDuplicated(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	proxy, err := Handler([]Route{{Prefix: "/", Target: backend.URL}})
	if err != nil {
		t.Fatal(err)
	}
	// The front door's own stamp, the same one serve.New applies in production.
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()

	resp, err := http.Get(front.URL + "/anything")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	for _, name := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy"} {
		if v := resp.Header.Values(name); len(v) != 1 {
			t.Errorf("%s arrived %d times (%v), want exactly one", name, len(v), v)
		}
	}
	// The backend's own headers are untouched, so stripping is surgical.
	if got := resp.Header.Get("Content-Type"); got != "text/plain" {
		t.Errorf("Content-Type = %q, want the backend's own", got)
	}
}
