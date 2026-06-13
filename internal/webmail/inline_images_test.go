package webmail

import (
	"bytes"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/mime"
)

// onePxPNG is a 1x1 transparent PNG, base64-encoded, used as an inline image.
const onePxPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

// TestInlineImageBecomesRelatedCidPart checks the compose-side inline-image path:
// a base64 data: <img> in the HTML body is sent as a multipart/related message
// whose HTML references the image by cid:, matched by an inline image part's
// Content-ID, with the original bytes carried and no data: URI left on the wire.
func TestInlineImageBecomesRelatedCidPart(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	body := `<p>see <img src="data:image/png;base64,` + onePxPNG + `"></p>`
	if code, _ := postForm(t, c, ts.URL+"/compose", url.Values{
		"action": {"send"}, "format": {"html"},
		"to": {"alice@hermex.test"}, "subject": {"inline pic"},
		"body": {"see"}, "bodyhtml": {body},
	}); code != 200 {
		t.Fatalf("send with inline image = %d", code)
	}

	raw := folderRaw(t, path, "Sent")
	if strings.Contains(raw, "data:image") {
		t.Errorf("a base64 data: URI was persisted on the wire:\n%s", raw)
	}
	root := mime.ParseStructure([]byte(raw))
	if len(collectParts(root, func(p *mime.Part) bool { return p.Type == "multipart" && p.Subtype == "related" })) == 0 {
		t.Fatalf("Sent copy is not multipart/related:\n%s", raw)
	}
	htmlPart := findPart(root, func(p *mime.Part) bool { return p.Type == "text" && p.Subtype == "html" })
	imgPart := findPart(root, func(p *mime.Part) bool { return p.Type == "image" })
	if htmlPart == nil || imgPart == nil {
		t.Fatalf("missing html or image part:\n%s", raw)
	}

	cid := strings.Trim(imgPart.ID, "<>")
	if cid == "" {
		t.Fatalf("inline image part carries no Content-ID:\n%s", raw)
	}
	hc, _ := htmlPart.DecodedContent()
	if !strings.Contains(string(hc), "cid:"+cid) {
		t.Errorf("HTML body does not reference the image by cid:%s\nhtml=%s", cid, hc)
	}

	// The inline part carries the original image bytes verbatim.
	want, _ := base64.StdEncoding.DecodeString(onePxPNG)
	if got, _ := imgPart.DecodedContent(); !bytes.Equal(got, want) {
		t.Errorf("inline image bytes differ: got %d bytes, want %d", len(got), len(want))
	}
}

// TestInlineImageDraftKeepsDataURI checks that saving a draft does NOT extract the
// inline image: the draft keeps the base64 data: URI in its HTML so the editor can
// redisplay it on reopen (the cid: rewrite is send-time only).
func TestInlineImageDraftKeepsDataURI(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	body := `<p>draft <img src="data:image/png;base64,` + onePxPNG + `"></p>`
	postForm(t, c, ts.URL+"/compose", url.Values{
		"action": {"savedraft"}, "format": {"html"},
		"subject": {"draft pic"}, "body": {"draft"}, "bodyhtml": {body},
	})

	drafts := folderMsgs(t, path, draftFID)
	if len(drafts) != 1 {
		t.Fatalf("Drafts has %d, want 1", len(drafts))
	}
	raw := msgRaw(t, path, draftFID, drafts[0].UID)
	if !strings.Contains(raw, "data:image/png;base64,") {
		t.Errorf("saved draft lost its inline data: URI (it must survive for editor redisplay):\n%s", raw)
	}
	if strings.Contains(raw, "cid:") {
		t.Errorf("saved draft must not rewrite inline images to cid: (that is send-time only):\n%s", raw)
	}
}
