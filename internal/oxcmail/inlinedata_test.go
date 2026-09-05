package oxcmail

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// onePixelPNG is the smallest real image, so the test asserts on bytes a decoder
// would accept rather than on a placeholder.
var onePixelPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89,
}

func dataURI(mimeType string, data []byte) string {
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func seqIDs(n int) string { return fmt.Sprintf("cid%d@hermex.test", n+1) }

// TestInlineDataURIsBecomeCidAttachments is the deliverability guarantee. A body
// that keeps its pictures as data: URIs renders for the sender and shows a gap
// for the recipient, because Outlook and Gmail refuse a data: image in received
// mail.
func TestInlineDataURIsBecomeCidAttachments(t *testing.T) {
	body := `<p>before</p><img src="` + dataURI("image/png", onePixelPNG) + `" alt="shot"><p>after</p>`

	out, atts, err := InlineDataURIs(body, seqIDs)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 {
		t.Fatalf("got %d attachments, want 1", len(atts))
	}
	if strings.Contains(out, "data:image/") {
		t.Errorf("the body still carries a data: image:\n%s", out)
	}
	if !strings.Contains(out, `src="cid:cid1@hermex.test"`) {
		t.Errorf("body does not reference the attachment:\n%s", out)
	}
	if !strings.Contains(out, `alt="shot"`) || !strings.Contains(out, "before") {
		t.Errorf("rewriting lost surrounding markup:\n%s", out)
	}

	assertInlinePNG(t, atts[0], "cid1@hermex.test")
}

// assertInlinePNG checks the property bag the exporter reads to decide the part
// is an inline, Content-ID-carrying picture.
func assertInlinePNG(t *testing.T, att Attachment, wantCID string) {
	t.Helper()
	p := att.Props
	if got, _ := bytesProp(p, mapi.PrAttachDataBin); string(got) != string(onePixelPNG) {
		t.Errorf("attachment bytes = % x", got)
	}
	if got := propString(p, mapi.PrAttachContentID); got != wantCID {
		t.Errorf("Content-ID = %q, want %q", got, wantCID)
	}
	if flags, _ := propInt32(p, mapi.PrAttachFlags); flags&mapi.AttMhtmlRef == 0 {
		t.Error("the attachment is not marked inline, so the exporter would not put it in a multipart/related")
	}
	if got := propString(p, mapi.PrAttachMimeTag); got != "image/png" {
		t.Errorf("mime tag = %q", got)
	}
	if got := propString(p, mapi.PrAttachLongFilename); got != "image001.png" {
		t.Errorf("filename = %q", got)
	}
}

// TestInlineDataURIsExportIntoMultipartRelated proves the whole path: the
// rewritten body and its attachments become the MIME shape a mail client
// renders, with the Content-ID matching what the body points at.
func TestInlineDataURIsExportIntoMultipartRelated(t *testing.T) {
	body := `<p>hi</p><img src="` + dataURI("image/png", onePixelPNG) + `">`
	out, atts, err := InlineDataURIs(body, seqIDs)
	if err != nil {
		t.Fatal(err)
	}

	var props mapi.PropertyValues
	props.Set(mapi.PrInternetMessageID, "<m1@hermex.test>")
	props.Set(mapi.PrSubject, "pasted picture")
	props.Set(mapi.PrHTML, []byte(out))
	props.Set(mapi.PrBody, "hi")
	raw, err := Export(&Message{Props: props, Attachments: atts}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// MIME header names are case-insensitive, and Go canonicalises this one to
	// "Content-Id" on the way out.
	mime := string(raw)
	for _, want := range []string{
		"multipart/related",
		"Content-Id: <cid1@hermex.test>",
		"Content-Disposition: inline",
		"Content-Type: image/png",
		"src=\"cid:cid1@hermex.test\"",
	} {
		if !strings.Contains(mime, want) {
			t.Errorf("exported message is missing %q:\n%s", want, mime)
		}
	}
	if strings.Contains(mime, "data:image/") {
		t.Error("the exported message still carries a data: image")
	}
}

// TestInlineDataURIsLeaveEverythingElseAlone keeps the rewrite from touching a
// body it has no business in.
func TestInlineDataURIsLeaveEverythingElseAlone(t *testing.T) {
	for _, body := range []string{
		`<p>no pictures at all</p>`,
		`<img src="https://example.org/a.png">`,
		`<img src="cid:already@hermex.test">`,
	} {
		out, atts, err := InlineDataURIs(body, seqIDs)
		if err != nil {
			t.Fatal(err)
		}
		if len(atts) != 0 || out != body {
			t.Errorf("%q was rewritten to %q with %d attachments", body, out, len(atts))
		}
	}
}

// TestInlineDataURIsKeepAnUndecodableImage proves a malformed data: URI stays as
// it was. Dropping the element would lose the picture the sender placed, and the
// sender cannot tell a corrupt payload from a missing one.
func TestInlineDataURIsKeepAnUndecodableImage(t *testing.T) {
	for _, src := range []string{
		"data:image/png;base64,%%%not base64%%%",
		"data:image/png,notbase64at all",
		"data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte("<b>x</b>")),
	} {
		body := `<img src="` + src + `">`
		out, atts, err := InlineDataURIs(body, seqIDs)
		if err != nil {
			t.Fatal(err)
		}
		if len(atts) != 0 {
			t.Errorf("%q produced an attachment", src)
		}
		if !strings.Contains(out, src) {
			t.Errorf("%q was dropped from the body: %s", src, out)
		}
	}
}

// TestInlineDataURIsAcceptAWrappedPayload covers the payload a browser wraps
// across lines in the markup, which strict base64 refuses.
func TestInlineDataURIsAcceptAWrappedPayload(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString(onePixelPNG)
	wrapped := enc[:8] + "\n   " + enc[8:]
	body := `<img src="data:image/png;base64,` + wrapped + `">`
	_, atts, err := InlineDataURIs(body, seqIDs)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 {
		t.Fatalf("a wrapped payload produced %d attachments, want 1", len(atts))
	}
}

// TestInlineDataURIsAreBounded keeps one body from turning into an unbounded
// number of in-memory attachments.
func TestInlineDataURIsAreBounded(t *testing.T) {
	one := `<img src="` + dataURI("image/png", onePixelPNG) + `">`
	body := strings.Repeat(one, maxInlineDataURIs+10)
	_, atts, err := InlineDataURIs(body, seqIDs)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != maxInlineDataURIs {
		t.Errorf("got %d attachments, want the cap of %d", len(atts), maxInlineDataURIs)
	}
}
