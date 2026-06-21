package oxcmail

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	stdmime "mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"sort"
	"strings"

	"hermex/internal/mapi"
)

// dateLayout is the RFC 5322 date format Export synthesizes (the wire form of
// strftime "%a, %d %b %Y %H:%M:%S %z").
const dateLayout = "Mon, 02 Jan 2006 15:04:05 -0700"

// Export renders a MAPI Message into a wire-format RFC 5322 / MIME message,
// implementing the MS-OXCMAIL MAPI-to-internet export path. It synthesizes fresh
// headers, MIME structure, and transfer encodings; it does not reproduce any
// original bytes (the Date and the chosen encodings are generated afresh, so the
// output is a valid message but not byte-identical to what was imported).
//
// Body skeletons: text/plain, text/html, or multipart/alternative; when the
// message has attachments the body is wrapped, with the attachments, in a
// multipart/mixed. The long tail (related/inline layout, embedded messages,
// calendar, TNEF, S/MIME) is deferred. opt is currently unused (the core
// property set carries no named properties).
func Export(msg *Message, opt Options) ([]byte, error) {
	var b bytes.Buffer
	writeMailHead(&b, msg)

	// Separate inline (HTML-referenced) attachments from regular ones: inline
	// images join the HTML body in a multipart/related, regular attachments wrap
	// everything in a multipart/mixed.
	var inline, regular []Attachment
	for _, att := range msg.Attachments {
		if isInlineAttachment(att) {
			inline = append(inline, att)
		} else {
			regular = append(regular, att)
		}
	}

	// The innermost unit is the body, wrapped in multipart/related when it has
	// inline images.
	innerHdr, innerBytes := renderBody(msg, opt)
	if len(inline) > 0 {
		var err error
		innerHdr, innerBytes, err = renderRelated(innerHdr, innerBytes, inline)
		if err != nil {
			return nil, err
		}
	}

	if len(regular) == 0 {
		writeHeaderFields(&b, innerHdr)
		b.WriteString("\r\n")
		b.Write(innerBytes)
		return b.Bytes(), nil
	}

	// Regular attachments wrap the inner unit in multipart/mixed.
	var parts bytes.Buffer
	mw := multipart.NewWriter(&parts)
	iw, err := mw.CreatePart(innerHdr)
	if err != nil {
		return nil, err
	}
	iw.Write(innerBytes)
	for _, att := range regular {
		ah, adata := renderAttachment(att)
		aw, err := mw.CreatePart(ah)
		if err != nil {
			return nil, err
		}
		aw.Write(adata)
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	writeField(&b, "Content-Type", "multipart/mixed; boundary=\""+mw.Boundary()+"\"")
	b.WriteString("\r\n")
	b.Write(parts.Bytes())
	return b.Bytes(), nil
}

// renderRelated wraps the body part and the inline attachments in a
// multipart/related part (HTML body with its referenced images).
func renderRelated(bodyHdr textproto.MIMEHeader, bodyBytes []byte, inline []Attachment) (textproto.MIMEHeader, []byte, error) {
	var parts bytes.Buffer
	mw := multipart.NewWriter(&parts)
	bw, err := mw.CreatePart(bodyHdr)
	if err != nil {
		return nil, nil, err
	}
	bw.Write(bodyBytes)
	for _, att := range inline {
		ah, adata := renderAttachment(att)
		aw, err := mw.CreatePart(ah)
		if err != nil {
			return nil, nil, err
		}
		aw.Write(adata)
	}
	if err := mw.Close(); err != nil {
		return nil, nil, err
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", "multipart/related; boundary=\""+mw.Boundary()+"\"")
	return h, parts.Bytes(), nil
}

// isInlineAttachment reports whether an attachment is an HTML-referenced inline
// image (PR_ATTACH_FLAGS carries ATT_MHTML_REF).
func isInlineAttachment(att Attachment) bool {
	flags, _ := propInt32(att.Props, mapi.PrAttachFlags)
	return flags&mapi.AttMhtmlRef != 0
}

// renderBody renders the message body as a MIME part: its header fields and its
// transfer-encoded content. The shape is multipart/alternative when both plain
// and HTML bodies are present, text/html when only HTML, text/plain otherwise.
// When opt carries a calendar body (an iTIP message), the body becomes a
// multipart/alternative whose final alternative is that text/calendar part.
func renderBody(msg *Message, opt Options) (textproto.MIMEHeader, []byte) {
	plain := propString(msg.Props, mapi.PrBody)
	html, hasHTML := bytesProp(msg.Props, mapi.PrHTML)
	if len(opt.CalendarBody) > 0 {
		return renderCalendarAlternative(plain, html, hasHTML, htmlCharset(msg.Props), opt.CalendarBody, opt.CalendarMethod)
	}
	switch {
	case hasHTML && plain != "":
		return renderAlternative(plain, html, htmlCharset(msg.Props))
	case hasHTML:
		return renderLeaf("text/html; charset="+htmlCharset(msg.Props), html)
	default:
		return renderLeaf("text/plain; charset=utf-8", []byte(plain))
	}
}

// renderCalendarAlternative builds a multipart/alternative whose alternatives are
// the text body (plain, and HTML when present) and the iTIP calendar part. A
// receiving client renders the text it understands or processes the calendar
// part; the method on the part's Content-Type must match the METHOD inside the
// iCalendar, so it is carried through rather than re-parsed.
func renderCalendarAlternative(plain string, html []byte, hasHTML bool, htmlCset string, ical []byte, method string) (textproto.MIMEHeader, []byte) {
	var parts bytes.Buffer
	mw := multipart.NewWriter(&parts)
	ph, penc := renderLeaf("text/plain; charset=utf-8", []byte(plain))
	if pw, err := mw.CreatePart(ph); err == nil {
		pw.Write(penc)
	}
	if hasHTML {
		hh, henc := renderLeaf("text/html; charset="+htmlCset, html)
		if hw, err := mw.CreatePart(hh); err == nil {
			hw.Write(henc)
		}
	}
	ct := "text/calendar; charset=utf-8"
	if method != "" {
		ct += "; method=" + method
	}
	ch, cenc := renderLeaf(ct, ical)
	if cw, err := mw.CreatePart(ch); err == nil {
		cw.Write(cenc)
	}
	mw.Close()
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", "multipart/alternative; boundary=\""+mw.Boundary()+"\"")
	return h, parts.Bytes()
}

// renderLeaf renders a single content part: a Content-Type and the
// transfer-encoded body with its Content-Transfer-Encoding.
func renderLeaf(contentType string, raw []byte) (textproto.MIMEHeader, []byte) {
	cte, enc := encodeForTransfer(raw)
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", contentType)
	h.Set("Content-Transfer-Encoding", cte)
	return h, enc
}

// renderAlternative renders a multipart/alternative part with a text/plain and a
// text/html alternative. The boundary is generated by the multipart writer.
func renderAlternative(plain string, html []byte, htmlCset string) (textproto.MIMEHeader, []byte) {
	var parts bytes.Buffer
	mw := multipart.NewWriter(&parts)
	ph, penc := renderLeaf("text/plain; charset=utf-8", []byte(plain))
	if pw, err := mw.CreatePart(ph); err == nil {
		pw.Write(penc)
	}
	hh, henc := renderLeaf("text/html; charset="+htmlCset, html)
	if hw, err := mw.CreatePart(hh); err == nil {
		hw.Write(henc)
	}
	mw.Close()
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", "multipart/alternative; boundary=\""+mw.Boundary()+"\"")
	return h, parts.Bytes()
}

// renderAttachment renders one attachment as a by-value MIME part: its content
// type and filename, disposition (inline for an HTML-referenced image), an
// optional content id, and base64-encoded data.
func renderAttachment(att Attachment) (textproto.MIMEHeader, []byte) {
	data, _ := bytesProp(att.Props, mapi.PrAttachDataBin)
	mimeType := propString(att.Props, mapi.PrAttachMimeTag)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	filename := propString(att.Props, mapi.PrAttachLongFilename)

	h := textproto.MIMEHeader{}
	ct := mimeType
	if filename != "" {
		ct += "; name=\"" + filename + "\""
	}
	h.Set("Content-Type", ct)

	// RFC 2046 §5.2.1: an encapsulated message (message/*) must use a 7bit,
	// 8bit, or binary transfer encoding, never base64 or quoted-printable, so
	// its bytes are emitted verbatim. Every other attachment is base64-encoded.
	cte, body := "base64", encodeBase64(data)
	if strings.HasPrefix(mimeType, "message/") {
		if is7bitClean(data) {
			cte = "7bit"
		} else {
			cte = "8bit"
		}
		body = data
	}
	h.Set("Content-Transfer-Encoding", cte)

	disposition := "attachment"
	if flags, _ := propInt32(att.Props, mapi.PrAttachFlags); flags&mapi.AttMhtmlRef != 0 {
		disposition = "inline"
	}
	if filename != "" {
		disposition += "; filename=\"" + filename + "\""
	}
	h.Set("Content-Disposition", disposition)
	if cid := propString(att.Props, mapi.PrAttachContentID); cid != "" {
		h.Set("Content-ID", "<"+cid+">")
	}
	return h, body
}

// writeHeaderFields writes a part's header fields in a fixed order; used for the
// top-level body when the message has no attachments.
func writeHeaderFields(b *bytes.Buffer, h textproto.MIMEHeader) {
	for _, k := range []string{"Content-Type", "Content-Transfer-Encoding", "Content-Disposition", "Content-ID"} {
		if v := h.Get(k); v != "" {
			writeField(b, k, v)
		}
	}
}

// encodeBase64 base64-encodes data, wrapped at 76 columns with CRLF.
func encodeBase64(data []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(data)
	var buf bytes.Buffer
	for len(enc) > 76 {
		buf.WriteString(enc[:76])
		buf.WriteString("\r\n")
		enc = enc[76:]
	}
	buf.WriteString(enc)
	buf.WriteString("\r\n")
	return buf.Bytes()
}

// writeMailHead synthesizes the message header block (no trailing blank line;
// the body writer appends the content headers and the separator), following the
// header order of the MS-OXCMAIL export path.
func writeMailHead(b *bytes.Buffer, msg *Message) {
	writeField(b, "MIME-Version", "1.0")

	// From carries the sent-representing identity.
	if from := identityAddress(msg.Props, representingTags); from != "" {
		writeField(b, "From", from)
	}
	// Sender is emitted only when it differs from the representing address.
	senderSMTP := propString(msg.Props, mapi.PrSenderSmtpAddress)
	reprSMTP := propString(msg.Props, mapi.PrSentRepresentingSmtpAddress)
	if senderSMTP != "" && !strings.EqualFold(senderSMTP, reprSMTP) {
		if s := identityAddress(msg.Props, senderTags); s != "" {
			writeField(b, "Sender", s)
		}
	}

	if to := recipientList(msg, mapi.RecipTo); to != "" {
		writeField(b, "To", to)
	}
	if cc := recipientList(msg, mapi.RecipCc); cc != "" {
		writeField(b, "Cc", cc)
	}
	if bcc := recipientList(msg, mapi.RecipBcc); bcc != "" {
		writeField(b, "Bcc", bcc)
	}

	if v, ok := propInt32(msg.Props, mapi.PrImportance); ok {
		writeField(b, "Importance", importanceText(v))
	}
	if v, ok := propInt32(msg.Props, mapi.PrSensitivity); ok {
		writeField(b, "Sensitivity", sensitivityText(v))
	}

	// A requested read receipt re-emits Disposition-Notification-To from the
	// read-receipt identity, falling back to the sender then the representing
	// identity, as the export path does.
	if requested, _ := propBool(msg.Props, mapi.PrReadReceiptRequested); requested {
		for _, t := range []addrTags{readReceiptTags, senderTags, representingTags} {
			if addr := identityAddress(msg.Props, t); addr != "" {
				writeField(b, "Disposition-Notification-To", addr)
				break
			}
		}
	}

	if v, ok := propUint64(msg.Props, mapi.PrClientSubmitTime); ok {
		writeField(b, "Date", mapi.NTTimeToUnix(v).UTC().Format(dateLayout))
	}

	writeField(b, "Subject", encodeText(subjectText(msg.Props)))

	if topic := propString(msg.Props, mapi.PrConversationTopic); topic != "" {
		writeField(b, "Thread-Topic", encodeText(topic))
	}
	if id := propString(msg.Props, mapi.PrInternetMessageID); id != "" {
		writeField(b, "Message-ID", id)
	}
	if refs := propString(msg.Props, mapi.PrInternetReferences); refs != "" {
		writeField(b, "References", refs)
	}
	if irt := propString(msg.Props, mapi.PrInReplyToID); irt != "" {
		writeField(b, "In-Reply-To", irt)
	}

	writePreservedHeaders(b, msg)
}

// preservedHeaderPrefixes are the inbound header families Export re-emits verbatim
// from the stored arrival headers, because the structured export reconstructs the
// message from MAPI properties and does not otherwise reproduce them. X-Spam-*
// carries the anti-spam verdict, which a client filters on.
var preservedHeaderPrefixes = []string{"x-spam-"}

// isPreservedHeader reports whether a header name belongs to a preserved family.
func isPreservedHeader(name string) bool {
	low := strings.ToLower(name)
	for _, p := range preservedHeaderPrefixes {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}

// writePreservedHeaders re-emits preserved inbound headers (e.g. X-Spam-*) from
// the stored arrival header block (PR_TRANSPORT_MESSAGE_HEADERS), which the
// structured export above does not reconstruct. Outbound messages carry no stored
// arrival headers, so nothing is emitted for them. Names are emitted in a stable
// order so the output is reproducible.
func writePreservedHeaders(b *bytes.Buffer, msg *Message) {
	raw := propString(msg.Props, mapi.PrTransportMessageHeaders)
	if raw == "" {
		return
	}
	tp := textproto.NewReader(bufio.NewReader(strings.NewReader(raw + "\r\n\r\n")))
	hdr, _ := tp.ReadMIMEHeader()
	names := make([]string, 0, len(hdr))
	for name := range hdr {
		if isPreservedHeader(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		for _, v := range hdr[name] {
			writeField(b, name, v)
		}
	}
}

// EnsureMessageID assigns PR_INTERNET_MESSAGE_ID to an originating message that
// has none, so the transmitted message carries an RFC 5322 Message-ID. A
// submission path calls it before Export, the way the reference assigns the id on
// the message rather than synthesizing it during serialization — Export itself
// only emits a present id. A message that already has one is left unchanged.
func EnsureMessageID(props *mapi.PropertyValues) {
	if propString(*props, mapi.PrInternetMessageID) != "" {
		return
	}
	if id := newMessageID(*props); id != "" {
		props.Set(mapi.PrInternetMessageID, id)
	}
}

// newMessageID mints an RFC 5322 Message-ID for an originating message that lacks
// one: a 128-bit random token at the sender's domain (sender, then representing
// identity, falling back to "localhost"). An empty result (the random source
// failed) tells the caller to leave the id unset rather than use a predictable one.
func newMessageID(props mapi.PropertyValues) string {
	host := "localhost"
	for _, t := range []addrTags{senderTags, representingTags} {
		if addr := identityAddress(props, t); addr != "" {
			// identityAddress may return a header-formatted value (e.g.
			// "<alice@example.com>"); strip the brackets before taking the domain.
			addr = strings.Trim(addr, "<> ")
			if i := strings.LastIndexByte(addr, '@'); i >= 0 && i < len(addr)-1 {
				host = addr[i+1:]
			}
			break
		}
	}
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return ""
	}
	return "<" + hex.EncodeToString(buf[:]) + "@" + host + ">"
}

// encodeForTransfer picks a content-transfer-encoding for raw and returns the
// encoding name and the encoded bytes: 7bit when the content is 7-bit-clean,
// quoted-printable otherwise.
func encodeForTransfer(raw []byte) (string, []byte) {
	if is7bitClean(raw) {
		return "7bit", raw
	}
	var buf bytes.Buffer
	w := quotedprintable.NewWriter(&buf)
	w.Write(raw)
	w.Close()
	return "quoted-printable", buf.Bytes()
}

// htmlCharset returns the charset name for the HTML body from PR_INTERNET_CPID,
// defaulting to UTF-8.
func htmlCharset(props mapi.PropertyValues) string {
	if v, ok := propInt32(props, mapi.PrInternetCodepage); ok {
		return cpidToCset(v)
	}
	return "utf-8"
}

// bytesProp returns a []byte-typed property and whether it was present.
func bytesProp(props mapi.PropertyValues, tag mapi.PropTag) ([]byte, bool) {
	if v, ok := props.Get(tag); ok {
		if b, ok := v.([]byte); ok {
			return b, true
		}
	}
	return nil, false
}

// identityAddress formats one identity (sender or sent-representing) as an
// address-list element ("Name <addr>"), or "" when no address is present.
func identityAddress(props mapi.PropertyValues, t addrTags) string {
	addr := propString(props, t.smtp)
	if addr == "" {
		addr = propString(props, t.email)
	}
	if addr == "" {
		return ""
	}
	a := mail.Address{Name: propString(props, t.name), Address: addr}
	return a.String()
}

// recipientList formats all recipients of one type as a comma-separated address
// list, or "" when there are none.
func recipientList(msg *Message, rcptType int32) string {
	var addrs []string
	for _, r := range msg.Recipients {
		if v, ok := propInt32(r, mapi.PrRecipientType); !ok || v != rcptType {
			continue
		}
		addr := propString(r, mapi.PrSmtpAddress)
		if addr == "" {
			addr = propString(r, mapi.PrEmailAddress)
		}
		if addr == "" {
			continue
		}
		a := mail.Address{Name: propString(r, mapi.PrDisplayName), Address: addr}
		addrs = append(addrs, a.String())
	}
	return strings.Join(addrs, ", ")
}

// subjectText reconstructs the Subject from the prefix and normalized subject
// (both are set on import), falling back to PR_SUBJECT.
func subjectText(props mapi.PropertyValues) string {
	prefix, hasPrefix := props.Get(mapi.PrSubjectPrefix)
	normalized, hasNorm := props.Get(mapi.PrNormalizedSubject)
	if hasPrefix && hasNorm {
		p, _ := prefix.(string)
		n, _ := normalized.(string)
		return p + n
	}
	return propString(props, mapi.PrSubject)
}

// importanceText maps PR_IMPORTANCE to its header value (always emitted).
func importanceText(v int32) string {
	switch v {
	case mapi.ImportanceLow:
		return "Low"
	case mapi.ImportanceHigh:
		return "High"
	}
	return "Normal"
}

// sensitivityText maps PR_SENSITIVITY to its header value (always emitted).
func sensitivityText(v int32) string {
	switch v {
	case mapi.SensitivityPersonal:
		return "Personal"
	case mapi.SensitivityPrivate:
		return "Private"
	case mapi.SensitivityConfidential:
		return "Company-Confidential"
	}
	return "Normal"
}

// encodeText returns the RFC 2047 encoded-word form of s when it contains
// non-ASCII or control characters, and s unchanged otherwise.
func encodeText(s string) string {
	return stdmime.QEncoding.Encode("utf-8", s)
}

// is7bitClean reports whether b is safe to emit as 7bit: only printable ASCII
// plus tab, CR, and LF.
func is7bitClean(b []byte) bool {
	for _, c := range b {
		if c == '\t' || c == '\r' || c == '\n' {
			continue
		}
		if c < 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}

// writeField writes one "Name: value" header line, CRLF-terminated.
func writeField(b *bytes.Buffer, name, value string) {
	b.WriteString(name)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteString("\r\n")
}

// propString returns a string-typed property, or "" when absent or not a string.
func propString(props mapi.PropertyValues, tag mapi.PropTag) string {
	if v, ok := props.Get(tag); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// propInt32 returns an int32-typed property and whether it was present.
func propInt32(props mapi.PropertyValues, tag mapi.PropTag) (int32, bool) {
	if v, ok := props.Get(tag); ok {
		if n, ok := v.(int32); ok {
			return n, true
		}
	}
	return 0, false
}

// propUint64 returns a uint64-typed property and whether it was present.
func propUint64(props mapi.PropertyValues, tag mapi.PropTag) (uint64, bool) {
	if v, ok := props.Get(tag); ok {
		if n, ok := v.(uint64); ok {
			return n, true
		}
	}
	return 0, false
}

// propBool returns a bool-typed property and whether it was present.
func propBool(props mapi.PropertyValues, tag mapi.PropTag) (bool, bool) {
	if v, ok := props.Get(tag); ok {
		if b, ok := v.(bool); ok {
			return b, true
		}
	}
	return false, false
}
