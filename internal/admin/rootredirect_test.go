package admin

import (
	"net/http"
	"testing"
)

// TestRootLeadsToThePanel: the panel published under its own host name is
// opened at that name's root, so the root and the bare /admin prefix redirect
// to the UI instead of answering 404. A path the panel does not serve still
// answers 404, so the redirect does not turn every unknown path into the UI.
func TestRootLeadsToThePanel(t *testing.T) {
	ts, _ := loggingAdminServer(t, systemAdminDir())
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string) *http.Response {
		resp, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		closeBody(t, resp.Body)
		return resp
	}
	for _, path := range []string{"/", "/admin", "/admin/"} {
		resp := get(path)
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/admin/ui/" {
			t.Errorf("GET %s: %d to %q, want 302 to /admin/ui/", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if resp := get("/no-such-page"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /no-such-page: %d, want 404", resp.StatusCode)
	}
}
