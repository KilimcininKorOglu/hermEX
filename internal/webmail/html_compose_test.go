package webmail

import (
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/mime"
	"hermex/internal/objectstore"
)

// TestWebmailHTMLComposeAlternative checks that composing in HTML mode produces
// a multipart/alternative message: the editor's plain rendering as the
// text/plain alternative and the markup as the text/html body, both surviving
// the store's parse/re-synthesize round trip.
func TestWebmailHTMLComposeAlternative(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")
	st, _ := objectstore.Open(path)
	st.Close()
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	resp, err := c.PostForm(ts.URL+"/compose", url.Values{
		"to":       {"alice@hermex.test"},
		"subject":  {"rich note"},
		"format":   {"html"},
		"body":     {"plain alternative text"},
		"bodyhtml": {"<p>rich <b>bold</b> body</p>"},
	})
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	raw := folderRaw(t, path, "Sent")
	root := mime.ParseStructure([]byte(raw))
	if root.Type != "multipart" || root.Subtype != "alternative" {
		t.Fatalf("Sent copy is %s/%s, want multipart/alternative:\n%s", root.Type, root.Subtype, raw)
	}
	var plain, html *mime.Part
	for _, ch := range root.Children {
		switch {
		case ch.Type == "text" && ch.Subtype == "plain":
			plain = ch
		case ch.Type == "text" && ch.Subtype == "html":
			html = ch
		}
	}
	if plain == nil || html == nil {
		t.Fatalf("alternative is missing a plain or html part:\n%s", raw)
	}
	if pc, _ := plain.DecodedContent(); !strings.Contains(string(pc), "plain alternative text") {
		t.Errorf("text/plain alternative = %q, want it to carry the plain rendering", pc)
	}
	if hc, _ := html.DecodedContent(); !strings.Contains(string(hc), "<b>bold</b>") {
		t.Errorf("text/html body lost its markup = %q", hc)
	}
}
