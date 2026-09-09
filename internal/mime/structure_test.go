package mime

import (
	"bytes"
	"fmt"
	"strconv"
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
	wantPartType(t, root, "multipart", "mixed", "the root part")
	if len(root.Children) != 2 {
		t.Fatalf("children = %d, want 2", len(root.Children))
	}

	text := root.Children[0]
	wantPartType(t, text, "text", "plain", "part 1")
	wantEq(t, text.Params["charset"], "utf-8", "part 1 charset")
	wantEq(t, text.Size, len("Hello body"), "part 1 size")

	att := root.Children[1]
	wantPartType(t, att, "application", "octet-stream", "part 2")
	wantEq(t, att.Encoding, "base64", "part 2 encoding")
	wantEq(t, att.Disposition, "attachment", "part 2 disposition")
	wantEq(t, att.DispParams["filename"], "a.bin", "part 2 filename")
	// Size is the ENCODED octet count ("QUJD" = 4), not the decoded length (3).
	wantEq(t, att.Size, 4, "part 2 size (the encoded length)")
}

func TestExtractSections(t *testing.T) {
	root := ParseStructure(mixedMsg)

	// BODY[] is the entire message, byte for byte.
	wantEq(t, string(mustExtract(t, root, Section{}, "BODY[]")), string(mixedMsg), "BODY[]")

	// Each leaf part's extracted body equals its reported Size, with the
	// boundary's CRLF excluded, distinct bytes per part catch off-by-CRLF.
	wantEq(t, string(mustExtract(t, root, Section{Path: []int{1}}, "BODY[1]")), "Hello body", "BODY[1]")
	wantEq(t, string(mustExtract(t, root, Section{Path: []int{2}}, "BODY[2]")), "QUJD", "BODY[2]")
	for i, child := range root.Children {
		b := mustExtract(t, root, Section{Path: []int{i + 1}}, "a child body")
		wantEq(t, len(b), child.Size, "the extracted length of child "+strconv.Itoa(i+1))
	}

	// Message-level HEADER includes the blank line; TEXT is everything after it.
	hdr := mustExtract(t, root, Section{Specifier: "HEADER"}, "HEADER")
	wantPrefix(t, hdr, "Subject: Test\r\n", "HEADER")
	wantSuffix(t, hdr, "\r\n\r\n", "HEADER")
	text := mustExtract(t, root, Section{Specifier: "TEXT"}, "TEXT")
	wantPrefix(t, text, "this is the preamble", "TEXT")
	// HEADER + TEXT reconstructs the whole message.
	wantEq(t, string(hdr)+string(text), string(mixedMsg), "HEADER + TEXT")

	// HEADER.FIELDS selects only the named field, plus the terminating blank.
	sub := mustExtract(t, root, Section{Specifier: "HEADER.FIELDS", Fields: []string{"Subject"}}, "HEADER.FIELDS")
	wantEq(t, string(sub), "Subject: Test\r\n\r\n", "HEADER.FIELDS (Subject)")
	// HEADER.FIELDS.NOT excludes it.
	not := mustExtract(t, root, Section{Specifier: "HEADER.FIELDS.NOT", Fields: []string{"Subject"}}, "HEADER.FIELDS.NOT")
	wantEq(t, bytes.Contains(not, []byte("Subject")), false, "HEADER.FIELDS.NOT carries the excluded field")
	wantEq(t, bytes.Contains(not, []byte("Content-Type")), true, "HEADER.FIELDS.NOT carries the other fields")

	// The attachment's MIME header (its own part header).
	mh := mustExtract(t, root, Section{Path: []int{2}, Specifier: "MIME"}, "BODY[2.MIME]")
	wantEq(t, bytes.Contains(mh, []byte("base64")), true, "BODY[2.MIME] carries the encoding")
	wantSuffix(t, mh, "\r\n\r\n", "BODY[2.MIME]")
}

// mustExtract extracts a section, stopping the test when it does not resolve.
func mustExtract(t *testing.T, root *Part, s Section, what string) []byte {
	t.Helper()
	b, ok := root.Extract(s)
	if !ok {
		t.Fatalf("%s did not resolve", what)
	}
	return b
}

// wantPrefix fails the test unless the extracted bytes begin with prefix.
func wantPrefix(t *testing.T, got []byte, prefix, what string) {
	t.Helper()
	if !bytes.HasPrefix(got, []byte(prefix)) {
		t.Errorf("%s = %q, want it to begin with %q", what, got, prefix)
	}
}

// wantSuffix fails the test unless the extracted bytes end with suffix.
func wantSuffix(t *testing.T, got []byte, suffix, what string) {
	t.Helper()
	if !bytes.HasSuffix(got, []byte(suffix)) {
		t.Errorf("%s = %q, want it to end with %q", what, got, suffix)
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
	wantPartType(t, embedded, "message", "rfc822", "part 2")
	if embedded.MsgEnvelope == nil {
		t.Fatal("the embedded message carries no envelope")
	}
	wantEq(t, embedded.MsgEnvelope.Subject, "Inner", "the embedded envelope's subject")
	if embedded.MsgBody == nil {
		t.Fatal("the embedded message carries no body part")
	}
	wantEq(t, embedded.MsgBody.Type, "text", "the embedded body part's type")

	// The encapsulated message's header and text are reachable through the
	// message/rfc822 part, and its single body is part 2.1.
	h := mustExtract(t, root, Section{Path: []int{2}, Specifier: "HEADER"}, "BODY[2.HEADER]")
	wantEq(t, bytes.Contains(h, []byte("Subject: Inner")), true, "BODY[2.HEADER] carries the inner subject")
	txt := mustExtract(t, root, Section{Path: []int{2}, Specifier: "TEXT"}, "BODY[2.TEXT]")
	wantEq(t, string(txt), "inner body", "BODY[2.TEXT]")
	wantEq(t, string(mustExtract(t, root, Section{Path: []int{2, 1}}, "BODY[2.1]")), "inner body", "BODY[2.1]")
}

// TestParseStructureDepthBound proves the parser refuses to descend past
// maxNestingDepth. The message is attacker-supplied on the unauthenticated SMTP
// path, and each level costs a stack frame, so an unbounded descent would let one
// message exhaust the goroutine stack and kill the process. The parse must return
// normally and stop nesting at the cap.
func TestParseStructureDepthBound(t *testing.T) {
	const levels = maxNestingDepth + 40

	var b bytes.Buffer
	for i := range levels {
		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=\"B%d\"\r\n\r\n--B%d\r\n", i, i)
	}
	b.WriteString("Content-Type: text/plain\r\n\r\ninnermost\r\n")
	for i := levels - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "--B%d--\r\n", i)
	}

	root := ParseStructure(b.Bytes())

	depth := 0
	for p := root; p != nil; depth++ {
		switch {
		case len(p.Children) > 0:
			p = p.Children[0]
		case p.MsgBody != nil:
			p = p.MsgBody
		default:
			p = nil
		}
	}
	if depth > maxNestingDepth+1 {
		t.Errorf("tree depth = %d, want at most %d", depth, maxNestingDepth+1)
	}
}
