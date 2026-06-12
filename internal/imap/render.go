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
	var sb strings.Builder
	sb.WriteByte('(')
	for _, a := range addrs {
		sb.WriteString(renderAddress(a))
	}
	sb.WriteByte(')')
	return sb.String()
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
func renderBodyStructure(p *mime.Part, extended bool) string {
	if p.Type == "multipart" {
		var sb strings.Builder
		sb.WriteByte('(')
		for _, ch := range p.Children {
			sb.WriteString(renderBodyStructure(ch, extended))
		}
		sb.WriteByte(' ')
		sb.WriteString(typeStr(p.Subtype))
		if extended {
			sb.WriteByte(' ')
			sb.WriteString(renderParamList(p.Params))
			sb.WriteByte(' ')
			sb.WriteString(renderDisposition(p))
			sb.WriteString(" NIL NIL") // body-fld-lang, body-fld-loc
		}
		sb.WriteByte(')')
		return sb.String()
	}

	var sb strings.Builder
	sb.WriteByte('(')
	sb.WriteString(typeStr(p.Type) + " " + typeStr(p.Subtype) + " ")
	sb.WriteString(renderParamList(p.Params) + " ")
	sb.WriteString(nstring(p.ID) + " " + nstring(p.Description) + " ")
	sb.WriteString(typeStr(p.Encoding) + " ")
	sb.WriteString(strconv.Itoa(p.Size))
	switch {
	case p.Type == "message" && p.Subtype == "rfc822":
		sb.WriteString(" " + renderEnvelope(p.MsgEnvelope))
		sb.WriteString(" " + renderBodyStructure(p.MsgBody, extended))
		sb.WriteString(" " + strconv.Itoa(p.Lines))
	case p.Type == "text":
		sb.WriteString(" " + strconv.Itoa(p.Lines))
	}
	if extended {
		sb.WriteString(" NIL ") // body-fld-md5
		sb.WriteString(renderDisposition(p))
		sb.WriteString(" NIL NIL") // body-fld-lang, body-fld-loc
	}
	sb.WriteByte(')')
	return sb.String()
}

// renderDisposition renders body-fld-dsp: NIL when absent, otherwise
// ("type" param-list).
func renderDisposition(p *mime.Part) string {
	if p.Disposition == "" {
		return "NIL"
	}
	return "(" + quoteString(p.Disposition) + " " + renderParamList(p.DispParams) + ")"
}
