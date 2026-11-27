// Package arc seals mail this server forwards with an Authenticated Received
// Chain set (RFC 8617). A forwarder that relays a message it did not author must
// not sign it as the author, so it records the authentication results it saw and
// vouches for them under its own domain. The package verifies an existing chain
// only to compute the chain validation status (cv=) of the set it adds; the
// receiving side never changes a delivery decision because of ARC.
package arc

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// field is one header field as it appears in the message: its name and the full
// raw text, folding and the terminating CRLF included.
type field struct {
	name string
	raw  string
}

// splitMessage separates the header block into fields and returns the body.
// Lines end in CRLF; a bare LF is accepted as an end of line too.
func splitMessage(raw []byte) ([]field, []byte) {
	var fields []field
	for i := 0; i < len(raw); {
		end := bytes.IndexByte(raw[i:], '\n')
		if end < 0 {
			return appendLine(fields, string(raw[i:])), nil
		}
		line := string(raw[i : i+end+1])
		i += end + 1
		if line == "\r\n" || line == "\n" {
			return fields, raw[i:]
		}
		fields = appendLine(fields, line)
	}
	return fields, nil
}

// appendLine adds a header line to fields: a continuation line joins the field
// above it, any other line starts a new field.
func appendLine(fields []field, line string) []field {
	if (line[0] == ' ' || line[0] == '\t') && len(fields) > 0 {
		fields[len(fields)-1].raw += line
		return fields
	}
	name, _, _ := strings.Cut(line, ":")
	return append(fields, field{name: strings.TrimSpace(name), raw: line})
}

// relaxedHeader canonicalizes one field by the relaxed algorithm (RFC 6376
// section 3.4.2): the name in lower case, the value unfolded, every run of
// whitespace reduced to one space, whitespace around the colon and at the end
// removed, and CRLF appended.
func relaxedHeader(raw string) string {
	name, value, _ := strings.Cut(raw, ":")
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r", ""), "\n", "")
	return strings.ToLower(strings.TrimSpace(name)) + ":" + strings.Join(strings.Fields(value), " ") + "\r\n"
}

// relaxedBody canonicalizes a body by the relaxed algorithm (RFC 6376 section
// 3.4.4): whitespace at the end of each line removed, every run of whitespace
// inside a line reduced to one space, empty lines at the end removed, and a
// non-empty body ended with CRLF.
func relaxedBody(body []byte) []byte {
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = compressWSP(l)
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.Join(lines, "\r\n") + "\r\n")
}

// compressWSP reduces each run of spaces and tabs in a line to one space and
// removes the whitespace at its end.
func compressWSP(l string) string {
	buf := make([]byte, 0, len(l))
	space := false
	for _, r := range l {
		if r == ' ' || r == '\t' {
			space = true
			continue
		}
		if space {
			buf = append(buf, ' ')
		}
		space = false
		buf = utf8.AppendRune(buf, r)
	}
	return string(buf)
}
