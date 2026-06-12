package imap

import (
	"strings"
	"testing"

	"hermex/internal/mime"
)

func TestRenderBodyStructureText(t *testing.T) {
	raw := []byte("Content-Type: text/plain; charset=us-ascii\r\n\r\nhello")
	got := renderBodyStructure(mime.ParseStructure(raw), false)
	want := `("TEXT" "PLAIN" ("CHARSET" "us-ascii") NIL NIL "7BIT" 5 0)`
	if got != want {
		t.Errorf("BODY = %s\nwant %s", got, want)
	}
}

func TestRenderBodyStructureMultipart(t *testing.T) {
	raw := []byte("Content-Type: multipart/mixed; boundary=\"B\"\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"hi\r\n" +
		"--B\r\n" +
		"Content-Type: application/pdf; name=\"a.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"a.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"QQ==\r\n" +
		"--B--\r\n")
	got := renderBodyStructure(mime.ParseStructure(raw), true)
	// Two nested part structures, then the subtype, then extension data.
	if !strings.HasPrefix(got, `(("TEXT" "PLAIN"`) {
		t.Errorf("multipart should start with the first child structure: %s", got)
	}
	if !strings.Contains(got, `"MIXED"`) {
		t.Errorf("missing multipart subtype: %s", got)
	}
	if !strings.Contains(got, `("attachment" ("FILENAME" "a.pdf"))`) {
		t.Errorf("missing attachment disposition: %s", got)
	}
	if !strings.Contains(got, `"BASE64" 4`) {
		t.Errorf("attachment should report base64 with encoded size 4: %s", got)
	}
}

func TestRenderEnvelopeNIL(t *testing.T) {
	raw := []byte("Subject: hi there\r\nFrom: Alice <alice@example.com>\r\n\r\nbody")
	env, err := mime.ParseEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	got := renderEnvelope(env)
	// Absent fields (date, cc, ...) are the atom NIL, never an empty quoted
	// string; From is a single address with a NIL source route.
	want := `(NIL "hi there" (("Alice" NIL "alice" "example.com")) ` +
		`(("Alice" NIL "alice" "example.com")) (("Alice" NIL "alice" "example.com")) ` +
		`NIL NIL NIL NIL NIL)`
	if got != want {
		t.Errorf("ENVELOPE = %s\nwant %s", got, want)
	}
}

func TestNStringEightBitBecomesLiteral(t *testing.T) {
	if got := nstring(""); got != "NIL" {
		t.Errorf("nstring(empty) = %q, want NIL", got)
	}
	if got := nstring("ascii"); got != `"ascii"` {
		t.Errorf("nstring(ascii) = %q, want quoted", got)
	}
	// 8-bit content cannot be a quoted string; it must be a literal.
	if got := nstring("üç"); !strings.HasPrefix(got, "{") {
		t.Errorf("nstring(8-bit) = %q, want a literal", got)
	}
}
