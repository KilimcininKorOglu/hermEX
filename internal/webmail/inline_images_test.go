package webmail

import (
	"bytes"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/mapi"
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

// TestReaderInlinesCIDImage checks the reader-side display: a received message
// whose HTML references an image by cid: is rendered with that image inlined as a
// data: URI (so it shows in the fully-sandboxed iframe with no credentialed
// subresource), the cid: reference is gone, and the inline image does not also
// appear in the downloadable attachment list.
func TestReaderInlinesCIDImage(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// Send an inline image to self; the compose path produces the cid: + related
	// form, and local delivery files a copy in the reader's INBOX.
	body := `<p>look <img src="data:image/png;base64,` + onePxPNG + `"></p>`
	postForm(t, c, ts.URL+"/compose", url.Values{
		"action": {"send"}, "format": {"html"},
		"to": {"alice@hermex.test"}, "subject": {"inline to read"},
		"body": {"look"}, "bodyhtml": {body},
	})

	inbox := folderMsgs(t, path, int64(mapi.PrivateFIDInbox))
	if len(inbox) != 1 {
		t.Fatalf("self-delivery put %d messages in INBOX, want 1", len(inbox))
	}

	// Assert on the reader detail directly: the served page HTML-escapes the body
	// into the srcdoc attribute (the browser reverses that on parse), so the raw
	// rewritten body is the faithful place to check the inlined bytes.
	raw := msgRaw(t, path, int64(mapi.PrivateFIDInbox), inbox[0].UID)
	d := buildMessageDetail([]byte(raw), "INBOX", inbox[0].UID, false)
	if !strings.Contains(d.Body, "data:image/png;base64,"+onePxPNG) {
		t.Errorf("reader did not inline the cid image as the original data: URI:\n%s", d.Body)
	}
	if strings.Contains(d.Body, "cid:") {
		t.Errorf("reader left an unresolved cid: reference in the body:\n%s", d.Body)
	}
	if len(d.Attachments) != 0 {
		t.Errorf("an inline image must not also be a downloadable attachment, got %d", len(d.Attachments))
	}

	// The served page carries the inlined image and keeps the iframe fully
	// sandboxed (no allow-same-origin: received HTML gets no credentialed request).
	_, page := get(t, c, ts.URL+"/message?folder=INBOX&uid="+itoa(inbox[0].UID))
	if !strings.Contains(page, "data:image/png;base64,") {
		t.Errorf("served reader page did not carry the inlined image")
	}
	if !strings.Contains(page, `sandbox=""`) {
		t.Errorf("reader iframe must stay fully sandboxed (sandbox=\"\")")
	}
}
