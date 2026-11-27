package imap

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"hermex/internal/mime"
)

// literalize renders arbitrary bytes as an IMAP literal ({n}CRLF + data). It is
// used for BODY[...] section data, which may be binary or contain CRLF.
func literalize(s string) string {
	return fmt.Sprintf("{%d}\r\n%s", len(s), s)
}

// quotable reports whether s may be sent as a quoted string: it must be 7-bit,
// free of CR/LF/NUL, and short enough to be reasonable inline.
func quotable(s string) bool {
	if len(s) > 1024 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 || s[i] == '\r' || s[i] == '\n' || s[i] == 0 {
			return false
		}
	}
	return true
}

// imapStr renders a non-empty string as a quoted string when safe, else as a
// literal (so 8-bit or long values stay protocol-legal).
func imapStr(s string) string {
	if quotable(s) {
		return quoteString(s)
	}
	return literalize(s)
}

// safeQuoted renders a mailbox name, an ACL identifier or an echoed command tag
// for an untagged response. It stays a quoted string, byte for byte as stored,
// so an 8-bit name keeps rendering exactly as before; a value carrying CR, LF or
// NUL cannot be quoted without breaking the response framing, so that one goes
// out as a length-prefixed literal and the client reads it as data instead of
// parsing an injected line. CREATE and RENAME refuse such a name, so this covers
// the names another protocol stored.
func safeQuoted(s string) string {
	if strings.ContainsAny(s, "\r\n\x00") {
		return literalize(s)
	}
	return quoteString(s)
}

// mailboxSafe reports whether a client-supplied mailbox name is free of the
// control characters that have no place in one (RFC 3501 ASTRING-CHAR excludes
// CTL). A literal argument carries raw octets, so CREATE/RENAME are where a CR
// or LF would otherwise enter the folder tree.
func mailboxSafe(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// nstring renders an IMAP nstring: the atom NIL for an absent value, otherwise
// a quoted string or literal. Clients branch on NIL vs "", so an empty value
// must be NIL, never an empty quoted string.
func nstring(s string) string {
	if s == "" {
		return "NIL"
	}
	return imapStr(s)
}

// typeStr renders a body type/subtype/encoding token: NIL when empty, else an
// uppercased quoted string (the conventional BODYSTRUCTURE form).
func typeStr(s string) string {
	if s == "" {
		return "NIL"
	}
	return quoteString(strings.ToUpper(s))
}

// renderParamList renders a Content-Type/disposition parameter list as
// ("KEY" "value" ...), or NIL when there are no parameters. Keys are sorted for
// deterministic output and uppercased; values are preserved verbatim.
func renderParamList(m map[string]string) string {
	if len(m) == 0 {
		return "NIL"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, quoteString(strings.ToUpper(k))+" "+imapStr(m[k]))
	}
	return "(" + strings.Join(parts, " ") + ")"
}

// renderAddress renders one ENVELOPE address as (name adl mailbox host); the
// at-domain-list (source route) is always NIL.
func renderAddress(a mime.Address) string {
	return "(" + nstring(a.Name) + " NIL " + nstring(a.Mailbox) + " " + nstring(a.Host) + ")"
}

// renderAddressList renders an ENVELOPE address list, or NIL when empty.
func renderAddressList(addrs []mime.Address) string {
	if len(addrs) == 0 {
		return "NIL"
	}
	var b []byte
	b = append(b, '(')
	for _, a := range addrs {
		b = append(b, renderAddress(a)...)
	}
	b = append(b, ')')
	return string(b)
}

// renderEnvelope renders an IMAP ENVELOPE structure. The date and subject use
// the raw header text (7-bit, encoded-words intact) as the protocol requires.
func renderEnvelope(e *mime.Envelope) string {
	if e == nil {
		return "(NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL)"
	}
	return "(" + nstring(e.RawDate) + " " + nstring(e.RawSubject) + " " +
		renderAddressList(e.From) + " " + renderAddressList(e.Sender) + " " +
		renderAddressList(e.ReplyTo) + " " + renderAddressList(e.To) + " " +
		renderAddressList(e.Cc) + " " + renderAddressList(e.Bcc) + " " +
		nstring(e.InReplyTo) + " " + nstring(e.MessageID) + ")"
}

// renderBodyStructure renders BODY (extended=false) or BODYSTRUCTURE
// (extended=true) for a part, recursing through multipart children and
// message/rfc822 encapsulation (RFC 3501 §7.4.2).
// renderEncapsulated renders the body of a message/rfc822 part. A part the parser
// did not descend into (nesting or part bound reached) has no parsed body, but
// RFC 3501 body-type-msg requires one, so the encapsulated message is described
// as one opaque text part of its size and line count.
func renderEncapsulated(p *mime.Part, extended bool) string {
	if p.MsgBody != nil {
		return renderBodyStructure(p.MsgBody, extended)
	}
	return `("TEXT" "PLAIN" NIL NIL NIL "7BIT" ` + strconv.Itoa(p.Size) + " " + strconv.Itoa(p.Lines) + ")"
}

func renderBodyStructure(p *mime.Part, extended bool) string {
	if p.Type == "multipart" {
		var b []byte
		b = append(b, '(')
		for _, ch := range p.Children {
			b = append(b, renderBodyStructure(ch, extended)...)
		}
		b = append(b, ' ')
		b = append(b, typeStr(p.Subtype)...)
		if extended {
			b = append(b, ' ')
			b = append(b, renderParamList(p.Params)...)
			b = append(b, ' ')
			b = append(b, renderDisposition(p)...)
			b = append(b, " NIL NIL"...) // body-fld-lang, body-fld-loc
		}
		b = append(b, ')')
		return string(b)
	}

	var b []byte
	b = append(b, '(')
	b = append(b, typeStr(p.Type)...)
	b = append(b, ' ')
	b = append(b, typeStr(p.Subtype)...)
	b = append(b, ' ')
	b = append(b, renderParamList(p.Params)...)
	b = append(b, ' ')
	b = append(b, nstring(p.ID)...)
	b = append(b, ' ')
	b = append(b, nstring(p.Description)...)
	b = append(b, ' ')
	b = append(b, typeStr(p.Encoding)...)
	b = append(b, ' ')
	b = strconv.AppendInt(b, int64(p.Size), 10)
	switch {
	case p.Type == "message" && p.Subtype == "rfc822":
		b = append(b, ' ')
		b = append(b, renderEnvelope(p.MsgEnvelope)...)
		b = append(b, ' ')
		b = append(b, renderEncapsulated(p, extended)...)
		b = append(b, ' ')
		b = strconv.AppendInt(b, int64(p.Lines), 10)
	case p.Type == "text":
		b = append(b, ' ')
		b = strconv.AppendInt(b, int64(p.Lines), 10)
	}
	if extended {
		b = append(b, " NIL "...) // body-fld-md5
		b = append(b, renderDisposition(p)...)
		b = append(b, " NIL NIL"...) // body-fld-lang, body-fld-loc
	}
	b = append(b, ')')
	return string(b)
}

// renderDisposition renders body-fld-dsp: NIL when absent, otherwise
// ("type" param-list).
func renderDisposition(p *mime.Part) string {
	if p.Disposition == "" {
		return "NIL"
	}
	return "(" + quoteString(p.Disposition) + " " + renderParamList(p.DispParams) + ")"
}
