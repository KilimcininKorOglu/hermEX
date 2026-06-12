package mime

import (
	"bytes"
	"strings"
)

// Section identifies a BODY[...] section to fetch (RFC 3501 §6.4.5). Path is
// the numeric part path (empty for the message itself). Specifier is one of
// "", "HEADER", "TEXT", "MIME", "HEADER.FIELDS", "HEADER.FIELDS.NOT"; Fields
// carries the header names for the two HEADER.FIELDS forms.
type Section struct {
	Path      []int
	Specifier string
	Fields    []string
}

// Extract returns the bytes of a message section, exactly as they appear in the
// source, or ok=false when the section does not address an existing part.
func (msg *Part) Extract(s Section) ([]byte, bool) {
	target := msg
	if len(s.Path) > 0 {
		t, ok := navigate(msg, s.Path)
		if !ok {
			return nil, false
		}
		target = t
	}

	switch strings.ToUpper(s.Specifier) {
	case "":
		// Whole message vs. whole part: a numeric path yields the part body,
		// the empty path yields the entire message (header + body).
		if len(s.Path) == 0 {
			return target.raw, true
		}
		return target.raw[target.bodyOffset:], true
	case "MIME":
		return target.raw[:target.bodyOffset], true
	case "HEADER":
		hdr := target
		if target.MsgBody != nil {
			hdr = target.MsgBody // message/rfc822: the encapsulated header
		}
		return hdr.raw[:hdr.bodyOffset], true
	case "TEXT":
		body := target
		if target.MsgBody != nil {
			body = target.MsgBody
		}
		return body.raw[body.bodyOffset:], true
	case "HEADER.FIELDS":
		return filterFields(headerOf(target), s.Fields, false), true
	case "HEADER.FIELDS.NOT":
		return filterFields(headerOf(target), s.Fields, true), true
	}
	return nil, false
}

// headerOf returns the header bytes (including the trailing blank line) to which
// a HEADER.FIELDS specifier applies: the encapsulated message's header for a
// message/rfc822 part, otherwise the part's own header.
func headerOf(p *Part) []byte {
	if p.MsgBody != nil {
		return p.MsgBody.raw[:p.MsgBody.bodyOffset]
	}
	return p.raw[:p.bodyOffset]
}

// PartAt returns the part addressed by a numeric path (empty path = the message
// itself), or ok=false when the path does not resolve.
func (msg *Part) PartAt(path []int) (*Part, bool) {
	if len(path) == 0 {
		return msg, true
	}
	return navigate(msg, path)
}

// navigate walks a numeric part path from the message root to the target part,
// descending through multipart children and message/rfc822 encapsulations.
func navigate(msg *Part, path []int) (*Part, bool) {
	cur := msg
	for i, n := range path {
		if n < 1 {
			return nil, false
		}
		switch {
		case len(cur.Children) > 0: // multipart
			if n > len(cur.Children) {
				return nil, false
			}
			cur = cur.Children[n-1]
		case cur.MsgBody != nil: // message/rfc822: descend into the encapsulated message
			enc := cur.MsgBody
			if len(enc.Children) > 0 {
				if n > len(enc.Children) {
					return nil, false
				}
				cur = enc.Children[n-1]
			} else if n == 1 {
				cur = enc
			} else {
				return nil, false
			}
		default: // single-part: only "1" is valid, and only as the final step
			if n != 1 || i != len(path)-1 {
				return nil, false
			}
		}
	}
	return cur, true
}

// filterFields returns the header lines whose field name is (exclude=false) or
// is not (exclude=true) in fields, preserving the original bytes and appending
// the terminating blank line. Field names are matched case-insensitively.
func filterFields(header []byte, fields []string, exclude bool) []byte {
	want := make(map[string]bool, len(fields))
	for _, f := range fields {
		want[strings.ToLower(f)] = true
	}
	var out bytes.Buffer
	for _, group := range headerLines(header) {
		name := fieldName(group)
		if want[strings.ToLower(name)] != exclude {
			out.Write(group)
		}
	}
	out.WriteString("\r\n")
	return out.Bytes()
}

// headerLines splits a header block into field groups, each group being a
// field's start line plus any folded continuation lines (those beginning with
// whitespace). The trailing blank line is not returned.
func headerLines(header []byte) [][]byte {
	var groups [][]byte
	var cur []byte
	for _, line := range splitKeepCRLF(header) {
		if len(bytes.TrimRight(line, "\r\n")) == 0 {
			break // blank line ends the header
		}
		if (line[0] == ' ' || line[0] == '\t') && cur != nil {
			cur = append(cur, line...) // folded continuation of the current field
			continue
		}
		if cur != nil {
			groups = append(groups, cur)
		}
		cur = append([]byte(nil), line...)
	}
	if cur != nil {
		groups = append(groups, cur)
	}
	return groups
}

// splitKeepCRLF splits data into lines, keeping each line's CRLF terminator.
func splitKeepCRLF(data []byte) [][]byte {
	var lines [][]byte
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			lines = append(lines, data)
			break
		}
		lines = append(lines, data[:i+1])
		data = data[i+1:]
	}
	return lines
}

// fieldName returns the header field name of a field group (the text before the
// first colon).
func fieldName(group []byte) string {
	if before, _, found := bytes.Cut(group, []byte{':'}); found {
		return strings.TrimSpace(string(before))
	}
	return ""
}
