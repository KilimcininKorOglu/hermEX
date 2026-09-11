package oxcmail

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"hermex/internal/mapi"
)

// mixedWith builds a multipart/mixed message whose parts are given verbatim, in
// the order given.
func mixedWith(parts ...string) []byte {
	var b strings.Builder
	b.WriteString("From: a@b.com\r\n" +
		"Subject: Ordered\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BB\"\r\n" +
		"\r\n")
	for _, p := range parts {
		b.WriteString("--BB\r\n")
		b.WriteString(p)
	}
	b.WriteString("--BB--\r\n")
	return []byte(b.String())
}

const (
	pdfPart = "Content-Type: application/pdf; name=\"a.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"a.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nJVBERi0=\r\n"
	plainPart = "Content-Type: text/plain; charset=utf-8\r\n\r\nplain body\r\n"
	htmlPart  = "Content-Type: text/html; charset=utf-8\r\n\r\n<p>html body</p>\r\n"
	imagePart = "Content-Type: image/png\r\n" +
		"Content-ID: <img1>\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\niVBORw0=\r\n"
	altPart = "Content-Type: multipart/alternative; boundary=\"CC\"\r\n\r\n" +
		"--CC\r\n" + plainPart +
		"--CC\r\n" + htmlPart +
		"--CC--\r\n"
)

// An HTML body does not have to be the first subpart of its container. A part
// that precedes it (an attachment, a plain part, an inline image) must not cost
// the message its HTML body, because the reader then shows the plain text, or
// nothing at all when the message carries no plain part.
func TestHTMLBodyIsTakenWhateverItsPosition(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts []string
	}{
		{"alternative first", []string{altPart, pdfPart}},
		{"attachment before the alternative", []string{pdfPart, altPart}},
		{"plain part before the alternative", []string{plainPart, altPart}},
		{"inline image before the alternative", []string{imagePart, altPart}},
		{"plain part before a bare html part", []string{plainPart, htmlPart}},
		{"attachment before a bare html part", []string{pdfPart, htmlPart}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := Import(mixedWith(tc.parts...), Options{})
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			html, ok := bytesProp(msg.Props, mapi.PrHTML)
			if !ok {
				t.Fatal("PR_HTML missing: the HTML body was dropped")
			}
			if !bytes.Contains(html, []byte("<p>html body</p>")) {
				t.Errorf("PR_HTML = %q, want the message's own markup", html)
			}
		})
	}
}

// Joined parts may carry different charsets, and one code page cannot label two
// of them, so the joined body is transcoded to UTF-8 and labelled UTF-8.
func TestJoinedHTMLPartsAreLabelledUTF8(t *testing.T) {
	latin := "Content-Type: text/html; charset=iso-8859-1\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\n<p>gr\xfc\xdfe</p>\r\n"
	utf := "Content-Type: text/html; charset=utf-8\r\n\r\n<p>grüße</p>\r\n"
	msg, err := Import(mixedWith(latin, utf), Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	html, ok := bytesProp(msg.Props, mapi.PrHTML)
	if !ok {
		t.Fatal("PR_HTML missing")
	}
	if !utf8.Valid(html) {
		t.Errorf("PR_HTML = %q, want valid UTF-8", html)
	}
	if bytes.Count(html, []byte("grüße")) != 2 {
		t.Errorf("PR_HTML = %q, want both parts transcoded to UTF-8", html)
	}
	if v, _ := propInt32(msg.Props, mapi.PrInternetCodepage); v != 65001 {
		t.Errorf("PR_INTERNET_CPID = %d, want 65001 (utf-8)", v)
	}
}

// Two HTML parts after a first HTML part are still joined, which is what the
// join rule exists for. Taking a later HTML body must not turn this into a
// single-part answer.
func TestSeveralHTMLPartsAreStillJoined(t *testing.T) {
	first := "Content-Type: text/html; charset=utf-8\r\n\r\n<p>first</p>\r\n"
	second := "Content-Type: text/html; charset=utf-8\r\n\r\n<p>second</p>\r\n"
	msg, err := Import(mixedWith(first, second), Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	html, ok := bytesProp(msg.Props, mapi.PrHTML)
	if !ok {
		t.Fatal("PR_HTML missing")
	}
	for _, want := range []string{"<p>first</p>", "<p>second</p>"} {
		if !bytes.Contains(html, []byte(want)) {
			t.Errorf("PR_HTML = %q, want it to carry %q", html, want)
		}
	}
}
