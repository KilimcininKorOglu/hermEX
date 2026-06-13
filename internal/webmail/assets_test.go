package webmail

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestEditorAssetsServed verifies the vendored rich-text editor assets are
// embedded and served locally. A self-hosted mail server must not depend on a
// CDN, so the editor's JS and CSS ship as static files in the binary.
func TestEditorAssetsServed(t *testing.T) {
	ts := newTestServer(t, seedMailbox(t))
	cases := []struct{ path, wantType string }{
		{"/static/quill.js", "javascript"},
		{"/static/quill.snow.css", "css"},
	}
	for _, a := range cases {
		resp, err := http.Get(ts.URL + a.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", a.path, resp.StatusCode)
		}
		if len(body) == 0 {
			t.Errorf("GET %s served an empty body", a.path)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, a.wantType) {
			t.Errorf("GET %s Content-Type = %q, want it to contain %q", a.path, ct, a.wantType)
		}
	}
}
