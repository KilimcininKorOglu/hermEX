package mime

import (
	"bufio"
	"bytes"
	stdmime "mime"
	"net/textproto"
	"strings"
)

// defaultContentType is the Content-Type assumed for an entity that declares
// none (RFC 2045 §5.2).
const defaultContentType = "text/plain"

// Part is one node of a parsed MIME tree. The exported fields carry everything
// an IMAP BODYSTRUCTURE response needs; the unexported fields retain the exact
// byte ranges (slices into the original message buffer) that BODY[...] section
// fetches return verbatim.
type Part struct {
	Type        string            // lowercased media type, e.g. "text", "multipart"
	Subtype     string            // lowercased subtype, e.g. "plain", "mixed"
	Params      map[string]string // Content-Type parameters, keys lowercased
	ID          string            // Content-ID, as written (with angle brackets)
	Description string            // Content-Description
	Encoding    string            // Content-Transfer-Encoding, lowercased; default "7bit"
	Disposition string            // Content-Disposition type, lowercased; "" if absent
	DispParams  map[string]string // Content-Disposition parameters, keys lowercased
	Size        int               // octets of the encoded body (as stored, not decoded)
	Lines       int               // body line count; meaningful for text/* and message/rfc822

	Children    []*Part   // multipart/* sub-parts, in order
	MsgEnvelope *Envelope // message/rfc822: the encapsulated message's envelope
	MsgBody     *Part     // message/rfc822: the encapsulated message's body structure

	raw        []byte // the entity's full bytes: header + blank line + body
	bodyOffset int    // index in raw at which the body begins (past the blank line)
}

// ParseStructure parses a complete RFC 5322 / MIME message into a Part tree.
// raw is taken as-is (CRLF line endings, as stored); no transfer decoding or
// line-ending normalization is performed, so section fetches stay byte-exact.
func ParseStructure(raw []byte) *Part {
	return parseEntity(raw)
}

// Header parses and returns this entity's header fields. Repeated fields are
// preserved in order under their canonical key. The parse is performed on each
// call; callers that iterate heavily should hold the result.
func (p *Part) Header() textproto.MIMEHeader {
	return parseHeader(p.raw[:p.bodyOffset])
}

// RawHeader returns this entity's header block verbatim: every byte from the
// start of the entity up to (and including) the blank line that separates the
// header from the body, exactly as stored. This is the form captured into
// PR_TRANSPORT_MESSAGE_HEADERS, which must preserve the original header bytes.
func (p *Part) RawHeader() []byte {
	return p.raw[:p.bodyOffset]
}

// parseEntity parses one MIME entity (a message or a body part): its header,
// then its body, recursing for multipart and message/rfc822 content.
func parseEntity(raw []byte) *Part {
	bodyOffset := headerEnd(raw)
	header := parseHeader(raw[:bodyOffset])
	body := raw[bodyOffset:]

	p := &Part{
		Encoding:   "7bit",
		raw:        raw,
		bodyOffset: bodyOffset,
		Size:       len(body),
	}

	mediaType, ctParams := parseMediaType(header.Get("Content-Type"), defaultContentType)
	p.Type, p.Subtype = splitMediaType(mediaType)
	p.Params = ctParams
	if enc := strings.TrimSpace(header.Get("Content-Transfer-Encoding")); enc != "" {
		p.Encoding = strings.ToLower(enc)
	}
	p.ID = strings.TrimSpace(header.Get("Content-ID"))
	p.Description = strings.TrimSpace(header.Get("Content-Description"))
	if disp := header.Get("Content-Disposition"); disp != "" {
		dispType, dispParams := parseMediaType(disp, "")
		p.Disposition = dispType
		p.DispParams = dispParams
	}

	switch {
	case p.Type == "multipart" && p.Params["boundary"] != "":
		for _, seg := range splitParts(body, p.Params["boundary"]) {
			p.Children = append(p.Children, parseEntity(seg))
		}
	case p.Type == "message" && p.Subtype == "rfc822":
		p.Lines = lineCount(body)
		p.MsgBody = parseEntity(body)
		if env, err := ParseEnvelope(body); err == nil {
			p.MsgEnvelope = env
		}
	default:
		if p.Type == "text" {
			p.Lines = lineCount(body)
		}
	}
	return p
}

// headerEnd returns the index at which an entity's body begins: just past the
// blank line that separates header from body. A message with no blank line is
// all header and has an empty body.
func headerEnd(raw []byte) int {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return i + 4
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return i + 2
	}
	return len(raw)
}

// parseHeader parses an entity's header block (which must end at the blank
// line). Malformed headers degrade to whatever could be read.
func parseHeader(header []byte) textproto.MIMEHeader {
	tr := textproto.NewReader(bufio.NewReader(bytes.NewReader(header)))
	h, err := tr.ReadMIMEHeader()
	if err != nil && len(h) == 0 {
		return textproto.MIMEHeader{}
	}
	return h
}

// parseMediaType parses a Content-Type/Content-Disposition value, falling back
// to fallback (which may be empty) when the value is missing or unparseable.
// Returned parameter keys are lowercased by the standard library.
func parseMediaType(v, fallback string) (string, map[string]string) {
	v = strings.TrimSpace(v)
	if v == "" {
		v = fallback
	}
	mt, params, err := stdmime.ParseMediaType(v)
	if err != nil {
		if fallback != "" {
			mt, _, _ = stdmime.ParseMediaType(fallback)
		}
		return strings.ToLower(mt), map[string]string{}
	}
	return strings.ToLower(mt), params
}

// splitMediaType splits "type/subtype" into its lowercased halves, defaulting a
// missing subtype to the empty string.
func splitMediaType(mt string) (string, string) {
	major, minor, found := strings.Cut(mt, "/")
	if !found {
		return major, ""
	}
	return major, minor
}

// lineCount returns the number of lines in a body, counted as LF occurrences.
func lineCount(body []byte) int {
	return bytes.Count(body, []byte("\n"))
}

// splitParts splits a multipart body into its constituent parts. Each returned
// slice is the exact bytes of one part (its header, blank line, and body) as a
// view into body; the CRLF preceding a boundary delimiter belongs to the
// delimiter (RFC 2046 §5.1.1) and is excluded, as are the preamble and epilogue.
func splitParts(body []byte, boundary string) [][]byte {
	dash := []byte("--" + boundary)
	var delims []int
	for i := 0; i+len(dash) <= len(body); {
		j := bytes.Index(body[i:], dash)
		if j < 0 {
			break
		}
		pos := i + j
		// A delimiter is recognized only at the start of a line.
		if pos == 0 || (pos >= 2 && body[pos-2] == '\r' && body[pos-1] == '\n') {
			delims = append(delims, pos)
		}
		i = pos + len(dash)
	}

	var parts [][]byte
	for k, pos := range delims {
		after := pos + len(dash)
		// "--boundary--" is the closing delimiter; the rest is epilogue.
		if after+1 < len(body) && body[after] == '-' && body[after+1] == '-' {
			break
		}
		// Skip transport padding up to the end of the delimiter line.
		nl := bytes.Index(body[after:], []byte("\r\n"))
		if nl < 0 {
			continue
		}
		partStart := after + nl + 2
		partEnd := len(body)
		if k+1 < len(delims) {
			partEnd = delims[k+1]
			if partEnd >= 2 && body[partEnd-2] == '\r' && body[partEnd-1] == '\n' {
				partEnd -= 2 // the CRLF before the next boundary is the delimiter's
			}
		}
		if partStart <= partEnd {
			parts = append(parts, body[partStart:partEnd])
		}
	}
	return parts
}
