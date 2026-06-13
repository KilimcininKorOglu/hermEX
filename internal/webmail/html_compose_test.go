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

// TestComposePageHasEditor checks that the compose page renders the rich-text
// editor scaffolding: the self-hosted editor assets, the editor container, the
// plain/HTML toggle, and the hidden fields the editor's JS populates on submit.
func TestComposePageHasEditor(t *testing.T) {
	ts := newTestServer(t, seedMailbox(t))
	c := authedClient(t, ts)
	code, body := get(t, c, ts.URL+"/compose")
	if code != 200 {
		t.Fatalf("GET /compose = %d", code)
	}
	for _, want := range []string{
		"/static/quill.js", "/static/quill.snow.css", // editor assets
		`id="editor"`,         // editor container
		`name="formatchoice"`, // plain/HTML toggle
		`name="format"`,       // submitted format selector
		`name="bodyhtml"`,     // submitted HTML body
		`id="composeform"`,    // form the editor JS hooks
	} {
		if !strings.Contains(body, want) {
			t.Errorf("compose page missing %q", want)
		}
	}
}

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
