package webmail2api

import (
	"bytes"
	"testing"
)

// A minimal valid header of each format the sniffer recognises, enough for
// http.DetectContentType to identify it.
var (
	pngBytes = []byte("\x89PNG\r\n\x1a\n" + "rest of the file")
	gifBytes = []byte("GIF89a" + "rest of the file")
	pdfBytes = []byte("%PDF-1.7\nrest of the file")
	htmlBody = []byte("<html><body><script>alert(1)</script></body></html>")
)

// TestDeclaredTypeIsNotTrustedOverTheBytes is the point of the check. The reader
// decides how to render an attachment from the served type: image/* goes in an
// <img>, application/pdf in an <object>. Serving the sender's own declaration hands
// that decision to the sender.
func TestDeclaredTypeIsNotTrustedOverTheBytes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared string
		body     []byte
	}{
		{"html claiming to be an image", "image/png", htmlBody},
		{"html claiming to be a pdf", "application/pdf", htmlBody},
		{"a pdf claiming to be an image", "image/png", pdfBytes},
		{"an image claiming to be a pdf", "application/pdf", pngBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := servedAttachmentType(tc.declared, tc.body); got != "application/octet-stream" {
				t.Errorf("served %q, want an opaque download", got)
			}
		})
	}
}

// TestActiveTypesAreNeverServedAsDeclared proves a sender cannot get the browser to
// treat their bytes as anything executable, whatever they write in the header.
func TestActiveTypesAreNeverServedAsDeclared(t *testing.T) {
	for _, declared := range []string{
		"text/html", "application/xhtml+xml", "image/svg+xml",
		"application/javascript", "text/javascript", "application/xml",
	} {
		if got := servedAttachmentType(declared, htmlBody); got != "application/octet-stream" {
			t.Errorf("declared %q served as %q, want an opaque download", declared, got)
		}
	}
}

// TestHonestPreviewTypesStillPreview guards the other direction: narrowing the rule
// must not stop a real image or a real PDF from rendering inline, which is the
// feature this whole endpoint feeds.
func TestHonestPreviewTypesStillPreview(t *testing.T) {
	for _, tc := range []struct {
		declared, body, want string
	}{
		{"application/pdf", string(pdfBytes), "application/pdf"},
		{"image/png", string(pngBytes), "image/png"},
		{"image/gif", string(gifBytes), "image/gif"},
	} {
		if got := servedAttachmentType(tc.declared, []byte(tc.body)); got != tc.want {
			t.Errorf("declared %q served as %q, want %q", tc.declared, got, tc.want)
		}
	}
}

// TestImageIsServedAsWhatItActuallyIs proves a mismatch WITHIN the image family is
// corrected rather than passed through. A sender declaring svg over PNG bytes gets
// image/png, so the type the browser acts on always describes the bytes it got.
func TestImageIsServedAsWhatItActuallyIs(t *testing.T) {
	if got := servedAttachmentType("image/svg+xml", pngBytes); got != "image/png" {
		t.Errorf("served %q, want image/png (what the bytes are)", got)
	}
	if got := servedAttachmentType("image/jpeg", gifBytes); got != "image/gif" {
		t.Errorf("served %q, want image/gif (what the bytes are)", got)
	}
}

// TestEmptyAttachmentIsOpaque proves a zero-length part cannot end up with a
// preview type. DetectContentType reports text/plain for empty input, and a part
// declaring image/png with no bytes must not be served as one.
func TestEmptyAttachmentIsOpaque(t *testing.T) {
	if got := servedAttachmentType("image/png", nil); got != "application/octet-stream" {
		t.Errorf("an empty attachment served as %q, want an opaque download", got)
	}
}

// TestServedTypeCarriesNoParameters proves the charset parameter the sniffer
// appends never reaches the header, since a Content-Type with a stray parameter is
// its own source of client confusion.
func TestServedTypeCarriesNoParameters(t *testing.T) {
	for _, body := range [][]byte{htmlBody, pngBytes, pdfBytes, nil} {
		if got := servedAttachmentType("image/png", body); bytes.ContainsAny([]byte(got), ";") {
			t.Errorf("served type %q carries a parameter", got)
		}
	}
}
