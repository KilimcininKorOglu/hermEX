package mime

import (
	"bytes"
	"testing"
)

// a multipart/mixed message with a text part and a base64 attachment, plus a
// preamble and epilogue to exercise their discarding.
var mixedMsg = []byte("Subject: Test\r\n" +
	"Content-Type: multipart/mixed; boundary=\"MIX\"\r\n" +
	"\r\n" +
	"this is the preamble\r\n" +
	"--MIX\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Hello body\r\n" +
	"--MIX\r\n" +
	"Content-Type: application/octet-stream; name=\"a.bin\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-Disposition: attachment; filename=\"a.bin\"\r\n" +
	"\r\n" +
	"QUJD\r\n" +
	"--MIX--\r\n" +
	"this is the epilogue\r\n")

func TestParseStructureMixed(t *testing.T) {
	root := ParseStructure(mixedMsg)
	if root.Type != "multipart" || root.Subtype != "mixed" {
		t.Fatalf("root = %s/%s, want multipart/mixed", root.Type, root.Subtype)
	}
	if len(root.Children) != 2 {
		t.Fatalf("children = %d, want 2", len(root.Children))
	}

	text := root.Children[0]
	if text.Type != "text" || text.Subtype != "plain" {
		t.Errorf("part 1 = %s/%s, want text/plain", text.Type, text.Subtype)
	}
	if text.Params["charset"] != "utf-8" {
		t.Errorf("part 1 charset = %q, want utf-8", text.Params["charset"])
	}
	if text.Size != len("Hello body") {
		t.Errorf("part 1 size = %d, want %d", text.Size, len("Hello body"))
	}

	att := root.Children[1]
	if att.Type != "application" || att.Subtype != "octet-stream" {
		t.Errorf("part 2 = %s/%s, want application/octet-stream", att.Type, att.Subtype)
	}
	if att.Encoding != "base64" {
		t.Errorf("part 2 encoding = %q, want base64", att.Encoding)
	}
	if att.Disposition != "attachment" || att.DispParams["filename"] != "a.bin" {
		t.Errorf("part 2 disposition = %q %v", att.Disposition, att.DispParams)
	}
	// Size is the ENCODED octet count ("QUJD" = 4), not the decoded length (3).
	if att.Size != 4 {
		t.Errorf("part 2 size = %d, want 4 (encoded length)", att.Size)
	}
}

func TestExtractSections(t *testing.T) {
	root := ParseStructure(mixedMsg)

	// BODY[] is the entire message, byte for byte.
	if whole, ok := root.Extract(Section{}); !ok || !bytes.Equal(whole, mixedMsg) {
		t.Fatalf("BODY[] mismatch (ok=%v, len %d vs %d)", ok, len(whole), len(mixedMsg))
	}

	// Each leaf part's extracted body equals its reported Size, with the
	// boundary's CRLF excluded — distinct bytes per part catch off-by-CRLF.
	if b, ok := root.Extract(Section{Path: []int{1}}); !ok || string(b) != "Hello body" {
		t.Errorf("BODY[1] = %q, want \"Hello body\"", b)
	}
	if b, ok := root.Extract(Section{Path: []int{2}}); !ok || string(b) != "QUJD" {
		t.Errorf("BODY[2] = %q, want \"QUJD\"", b)
	}
	for i, child := range root.Children {
		b, ok := root.Extract(Section{Path: []int{i + 1}})
		if !ok || len(b) != child.Size {
			t.Errorf("BODY[%d] length %d != reported size %d", i+1, len(b), child.Size)
		}
	}

	// Message-level HEADER includes the blank line; TEXT is everything after it.
	hdr, _ := root.Extract(Section{Specifier: "HEADER"})
	if !bytes.HasPrefix(hdr, []byte("Subject: Test\r\n")) || !bytes.HasSuffix(hdr, []byte("\r\n\r\n")) {
		t.Errorf("HEADER = %q", hdr)
	}
	text, _ := root.Extract(Section{Specifier: "TEXT"})
	if !bytes.HasPrefix(text, []byte("this is the preamble")) {
		t.Errorf("TEXT should start at the body: %q", text[:20])
	}
	// HEADER + TEXT reconstructs the whole message.
	if !bytes.Equal(append(append([]byte{}, hdr...), text...), mixedMsg) {
		t.Errorf("HEADER + TEXT != whole message")
	}

	// HEADER.FIELDS selects only the named field, plus the terminating blank.
	sub, _ := root.Extract(Section{Specifier: "HEADER.FIELDS", Fields: []string{"Subject"}})
	if string(sub) != "Subject: Test\r\n\r\n" {
		t.Errorf("HEADER.FIELDS (Subject) = %q", sub)
	}
	// HEADER.FIELDS.NOT excludes it.
	not, _ := root.Extract(Section{Specifier: "HEADER.FIELDS.NOT", Fields: []string{"Subject"}})
	if bytes.Contains(not, []byte("Subject")) || !bytes.Contains(not, []byte("Content-Type")) {
		t.Errorf("HEADER.FIELDS.NOT (Subject) = %q", not)
	}

	// The attachment's MIME header (its own part header).
	mh, _ := root.Extract(Section{Path: []int{2}, Specifier: "MIME"})
	if !bytes.Contains(mh, []byte("base64")) || !bytes.HasSuffix(mh, []byte("\r\n\r\n")) {
		t.Errorf("BODY[2.MIME] = %q", mh)
	}
}

func TestExtractMissingPart(t *testing.T) {
	root := ParseStructure(mixedMsg)
	if _, ok := root.Extract(Section{Path: []int{9}}); ok {
		t.Errorf("BODY[9] should not resolve")
	}
}

var nestedMsg = []byte("Subject: Outer\r\n" +
	"Content-Type: multipart/mixed; boundary=\"OUT\"\r\n" +
	"\r\n" +
	"--OUT\r\n" +
	"Content-Type: text/plain\r\n" +
	"\r\n" +
	"outer text\r\n" +
	"--OUT\r\n" +
	"Content-Type: message/rfc822\r\n" +
	"\r\n" +
	"From: inner@example.com\r\n" +
	"Subject: Inner\r\n" +
	"\r\n" +
	"inner body\r\n" +
	"--OUT--\r\n")

func TestParseStructureNestedMessage(t *testing.T) {
	root := ParseStructure(nestedMsg)
	if len(root.Children) != 2 {
		t.Fatalf("children = %d, want 2", len(root.Children))
	}
	embedded := root.Children[1]
	if embedded.Type != "message" || embedded.Subtype != "rfc822" {
		t.Fatalf("part 2 = %s/%s, want message/rfc822", embedded.Type, embedded.Subtype)
	}
	if embedded.MsgEnvelope == nil || embedded.MsgEnvelope.Subject != "Inner" {
		t.Errorf("embedded envelope = %+v", embedded.MsgEnvelope)
	}
	if embedded.MsgBody == nil || embedded.MsgBody.Type != "text" {
		t.Errorf("embedded body part = %+v", embedded.MsgBody)
	}

	// The encapsulated message's header and text are reachable through the
	// message/rfc822 part, and its single body is part 2.1.
	if h, _ := root.Extract(Section{Path: []int{2}, Specifier: "HEADER"}); !bytes.Contains(h, []byte("Subject: Inner")) {
		t.Errorf("BODY[2.HEADER] = %q", h)
	}
	if txt, _ := root.Extract(Section{Path: []int{2}, Specifier: "TEXT"}); string(txt) != "inner body" {
		t.Errorf("BODY[2.TEXT] = %q, want \"inner body\"", txt)
	}
	if b, ok := root.Extract(Section{Path: []int{2, 1}}); !ok || string(b) != "inner body" {
		t.Errorf("BODY[2.1] = %q (ok=%v), want \"inner body\"", b, ok)
	}
}
