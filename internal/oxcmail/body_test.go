package oxcmail

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/mime"
)

// altVector is a multipart/alternative message carrying a plain and an HTML
// representation.
var altVector = []byte("From: a@b.com\r\n" +
	"Subject: Alt\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/alternative; boundary=\"BB\"\r\n" +
	"\r\n" +
	"--BB\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"plain body\r\n" +
	"--BB\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>html body</p>\r\n" +
	"--BB--\r\n")

// TestImportAlternative checks that a multipart/alternative message yields a
// plain body (PR_BODY), an HTML body (PR_HTML, raw bytes), and the HTML code
// page (PR_INTERNET_CPID).
func TestImportAlternative(t *testing.T) {
	msg, err := Import(altVector, Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := propString(msg.Props, mapi.PrBody); !bytes.Contains([]byte(got), []byte("plain body")) {
		t.Errorf("PR_BODY = %q, want it to contain %q", got, "plain body")
	}
	html, ok := bytesProp(msg.Props, mapi.PrHTML)
	if !ok {
		t.Fatal("PR_HTML missing")
	}
	if !bytes.Contains(html, []byte("<p>html body</p>")) {
		t.Errorf("PR_HTML = %q, want it to contain the markup", html)
	}
	if v, _ := propInt32(msg.Props, mapi.PrInternetCodepage); v != 65001 {
		t.Errorf("PR_INTERNET_CPID = %d, want 65001 (utf-8)", v)
	}
}

// TestExportAlternativeRoundTrip checks that an alternative message exports to a
// well-formed multipart/alternative (text/plain + text/html) and re-imports to
// the same body properties.
func TestExportAlternativeRoundTrip(t *testing.T) {
	msg1, err := Import(altVector, Options{})
	if err != nil {
		t.Fatalf("Import 1: %v", err)
	}
	wire, err := Export(msg1, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Structural check: the exported tree is multipart/alternative with a
	// text/plain then a text/html part.
	tree := mime.ParseStructure(wire)
	if tree.Type != "multipart" || tree.Subtype != "alternative" {
		t.Fatalf("exported top-level = %s/%s, want multipart/alternative", tree.Type, tree.Subtype)
	}
	if len(tree.Children) != 2 {
		t.Fatalf("exported parts = %d, want 2", len(tree.Children))
	}
	if tree.Children[0].Type != "text" || tree.Children[0].Subtype != "plain" {
		t.Errorf("part 0 = %s/%s, want text/plain", tree.Children[0].Type, tree.Children[0].Subtype)
	}
	if tree.Children[1].Type != "text" || tree.Children[1].Subtype != "html" {
		t.Errorf("part 1 = %s/%s, want text/html", tree.Children[1].Type, tree.Children[1].Subtype)
	}

	// Body properties survive the round-trip.
	msg2, err := Import(wire, Options{})
	if err != nil {
		t.Fatalf("Import 2: %v", err)
	}
	if propString(msg1.Props, mapi.PrBody) != propString(msg2.Props, mapi.PrBody) {
		t.Errorf("PR_BODY drifted: %q -> %q",
			propString(msg1.Props, mapi.PrBody), propString(msg2.Props, mapi.PrBody))
	}
	h1, _ := bytesProp(msg1.Props, mapi.PrHTML)
	h2, _ := bytesProp(msg2.Props, mapi.PrHTML)
	if !bytes.Equal(h1, h2) {
		t.Errorf("PR_HTML drifted: %q -> %q", h1, h2)
	}
	c1, _ := propInt32(msg1.Props, mapi.PrInternetCodepage)
	c2, _ := propInt32(msg2.Props, mapi.PrInternetCodepage)
	if c1 != c2 {
		t.Errorf("PR_INTERNET_CPID drifted: %d -> %d", c1, c2)
	}
}

// mixedVector is a multipart/mixed message with a plain body and one attachment.
var mixedVector = []byte("From: a@b.com\r\n" +
	"Subject: WithAttach\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"MM\"\r\n" +
	"\r\n" +
	"--MM\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"see attached\r\n" +
	"--MM\r\n" +
	"Content-Type: text/plain; name=\"note.txt\"\r\n" +
	"Content-Disposition: attachment; filename=\"note.txt\"\r\n" +
	"\r\n" +
	"attachment content\r\n" +
	"--MM--\r\n")

// TestImportMixedAttachment checks that a multipart/mixed message yields a plain
// body and one by-value attachment with its MIME type, filename, and data.
func TestImportMixedAttachment(t *testing.T) {
	msg, err := Import(mixedVector, Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := propString(msg.Props, mapi.PrBody); !bytes.Contains([]byte(got), []byte("see attached")) {
		t.Errorf("PR_BODY = %q, want it to contain the body", got)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(msg.Attachments))
	}
	att := msg.Attachments[0]
	if got := propString(att.Props, mapi.PrAttachMimeTag); got != "text/plain" {
		t.Errorf("PR_ATTACH_MIME_TAG = %q", got)
	}
	if got := propString(att.Props, mapi.PrAttachLongFilename); got != "note.txt" {
		t.Errorf("PR_ATTACH_LONG_FILENAME = %q", got)
	}
	if v, _ := propInt32(att.Props, mapi.PrAttachMethod); v != mapi.AttachByValue {
		t.Errorf("PR_ATTACH_METHOD = %d, want ATTACH_BY_VALUE", v)
	}
	data, ok := bytesProp(att.Props, mapi.PrAttachDataBin)
	if !ok || !bytes.Contains(data, []byte("attachment content")) {
		t.Errorf("PR_ATTACH_DATA_BIN = %q", data)
	}
}

// TestExportMixedRoundTrip checks that a message with an attachment exports to a
// well-formed multipart/mixed (body part + attachment part) and re-imports to
// the same body and attachment data.
func TestExportMixedRoundTrip(t *testing.T) {
	msg1, err := Import(mixedVector, Options{})
	if err != nil {
		t.Fatalf("Import 1: %v", err)
	}
	wire, err := Export(msg1, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	tree := mime.ParseStructure(wire)
	if tree.Type != "multipart" || tree.Subtype != "mixed" {
		t.Fatalf("exported top-level = %s/%s, want multipart/mixed", tree.Type, tree.Subtype)
	}
	if len(tree.Children) != 2 {
		t.Fatalf("exported parts = %d, want 2 (body + attachment)", len(tree.Children))
	}

	msg2, err := Import(wire, Options{})
	if err != nil {
		t.Fatalf("Import 2: %v", err)
	}
	if propString(msg1.Props, mapi.PrBody) != propString(msg2.Props, mapi.PrBody) {
		t.Errorf("PR_BODY drifted")
	}
	if len(msg2.Attachments) != 1 {
		t.Fatalf("re-imported attachments = %d, want 1", len(msg2.Attachments))
	}
	d1, _ := bytesProp(msg1.Attachments[0].Props, mapi.PrAttachDataBin)
	d2, _ := bytesProp(msg2.Attachments[0].Props, mapi.PrAttachDataBin)
	if !bytes.Equal(d1, d2) {
		t.Errorf("attachment data drifted: %q -> %q", d1, d2)
	}
	if propString(msg1.Attachments[0].Props, mapi.PrAttachLongFilename) !=
		propString(msg2.Attachments[0].Props, mapi.PrAttachLongFilename) {
		t.Errorf("attachment filename drifted")
	}
}

// relatedVector is a multipart/related message: an HTML body that references an
// inline image by Content-ID.
var relatedVector = []byte("From: a@b.com\r\n" +
	"Subject: Inline\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/related; boundary=\"RR\"\r\n" +
	"\r\n" +
	"--RR\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<img src=\"cid:img1\">\r\n" +
	"--RR\r\n" +
	"Content-Type: image/png\r\n" +
	"Content-ID: <img1>\r\n" +
	"Content-Disposition: inline\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"iVBORw0KGgo=\r\n" +
	"--RR--\r\n")

// TestImportRelatedInline checks that a multipart/related message yields an HTML
// body and an inline image attachment flagged ATT_MHTML_REF with its Content-ID.
func TestImportRelatedInline(t *testing.T) {
	msg, err := Import(relatedVector, Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if _, ok := bytesProp(msg.Props, mapi.PrHTML); !ok {
		t.Error("PR_HTML missing")
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(msg.Attachments))
	}
	att := msg.Attachments[0]
	if got := propString(att.Props, mapi.PrAttachContentID); got != "img1" {
		t.Errorf("PR_ATTACH_CONTENT_ID = %q, want img1", got)
	}
	if got := propString(att.Props, mapi.PrAttachMimeTag); got != "image/png" {
		t.Errorf("PR_ATTACH_MIME_TAG = %q", got)
	}
	if flags, _ := propInt32(att.Props, mapi.PrAttachFlags); flags&mapi.AttMhtmlRef == 0 {
		t.Errorf("PR_ATTACH_FLAGS = %d, want ATT_MHTML_REF set", flags)
	}
}

// TestExportRelatedRoundTrip checks that an inline image exports to a
// multipart/related (HTML + image) and re-imports to the same inline attachment.
func TestExportRelatedRoundTrip(t *testing.T) {
	msg1, err := Import(relatedVector, Options{})
	if err != nil {
		t.Fatalf("Import 1: %v", err)
	}
	wire, err := Export(msg1, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	tree := mime.ParseStructure(wire)
	if tree.Type != "multipart" || tree.Subtype != "related" {
		t.Fatalf("exported top-level = %s/%s, want multipart/related", tree.Type, tree.Subtype)
	}
	if len(tree.Children) != 2 {
		t.Fatalf("exported parts = %d, want 2 (html + image)", len(tree.Children))
	}

	msg2, err := Import(wire, Options{})
	if err != nil {
		t.Fatalf("Import 2: %v", err)
	}
	if len(msg2.Attachments) != 1 {
		t.Fatalf("re-imported attachments = %d, want 1", len(msg2.Attachments))
	}
	if propString(msg1.Attachments[0].Props, mapi.PrAttachContentID) !=
		propString(msg2.Attachments[0].Props, mapi.PrAttachContentID) {
		t.Errorf("Content-ID drifted")
	}
	f1, _ := propInt32(msg1.Attachments[0].Props, mapi.PrAttachFlags)
	f2, _ := propInt32(msg2.Attachments[0].Props, mapi.PrAttachFlags)
	if f1 != f2 {
		t.Errorf("attachment flags drifted: %d -> %d", f1, f2)
	}
	d1, _ := bytesProp(msg1.Attachments[0].Props, mapi.PrAttachDataBin)
	d2, _ := bytesProp(msg2.Attachments[0].Props, mapi.PrAttachDataBin)
	if !bytes.Equal(d1, d2) {
		t.Errorf("inline image data drifted")
	}
}

// TestHTMLNonUTF8Charset checks that a non-UTF-8 HTML body keeps its raw bytes
// and code page across the convert path: the HTML is stored verbatim (not
// transcoded) and the charset round-trips through PR_INTERNET_CPID.
func TestHTMLNonUTF8Charset(t *testing.T) {
	// 0xe9 is "é" in ISO-8859-1; it must survive as a raw byte, not be decoded.
	raw := []byte("From: a@b.com\r\n" +
		"Subject: Latin\r\n" +
		"Content-Type: text/html; charset=iso-8859-1\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n" +
		"\r\n" +
		"<p>caf\xe9</p>\r\n")

	msg1, err := Import(raw, Options{})
	if err != nil {
		t.Fatalf("Import 1: %v", err)
	}
	h1, ok := bytesProp(msg1.Props, mapi.PrHTML)
	if !ok || !bytes.Contains(h1, []byte{0xe9}) {
		t.Fatalf("PR_HTML = %q, want the raw 0xe9 byte preserved", h1)
	}
	if v, _ := propInt32(msg1.Props, mapi.PrInternetCodepage); v != 28591 {
		t.Fatalf("PR_INTERNET_CPID = %d, want 28591 (iso-8859-1)", v)
	}

	wire, err := Export(msg1, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !bytes.Contains(wire, []byte("charset=iso-8859-1")) {
		t.Errorf("exported message lost the iso-8859-1 charset label")
	}

	msg2, err := Import(wire, Options{})
	if err != nil {
		t.Fatalf("Import 2: %v", err)
	}
	h2, _ := bytesProp(msg2.Props, mapi.PrHTML)
	if !bytes.Equal(h1, h2) {
		t.Errorf("PR_HTML drifted: %q -> %q", h1, h2)
	}
	c2, _ := propInt32(msg2.Props, mapi.PrInternetCodepage)
	if c2 != 28591 {
		t.Errorf("PR_INTERNET_CPID round-trip = %d, want 28591", c2)
	}
}

// TestExportHTMLOnly checks a message with only an HTML body: import records
// PR_HTML, export emits a single text/html body, and the HTML survives.
func TestExportHTMLOnly(t *testing.T) {
	raw := []byte("From: a@b.com\r\n" +
		"Subject: HTMLonly\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<h1>only html</h1>\r\n")

	msg1, err := Import(raw, Options{})
	if err != nil {
		t.Fatalf("Import 1: %v", err)
	}
	if msg1.Props.Has(mapi.PrBody) {
		t.Error("PR_BODY should be absent for an HTML-only message")
	}
	if _, ok := bytesProp(msg1.Props, mapi.PrHTML); !ok {
		t.Fatal("PR_HTML missing")
	}

	wire, err := Export(msg1, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	tree := mime.ParseStructure(wire)
	if tree.Type != "text" || tree.Subtype != "html" {
		t.Fatalf("exported top-level = %s/%s, want text/html", tree.Type, tree.Subtype)
	}

	msg2, err := Import(wire, Options{})
	if err != nil {
		t.Fatalf("Import 2: %v", err)
	}
	h1, _ := bytesProp(msg1.Props, mapi.PrHTML)
	h2, _ := bytesProp(msg2.Props, mapi.PrHTML)
	if !bytes.Equal(h1, h2) {
		t.Errorf("PR_HTML drifted: %q -> %q", h1, h2)
	}
}
