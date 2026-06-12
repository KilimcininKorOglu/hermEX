package oxcmail

import (
	"bytes"
	stdmime "mime"
	"mime/quotedprintable"
	"net/mail"
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
// The core path emits a single text/plain body; multipart bodies
// (alternative/mixed/related) and HTML are widened in a later slice. opt is
// currently unused (the core property set carries no named properties).
func Export(msg *Message, opt Options) ([]byte, error) {
	var b bytes.Buffer
	writeMailHead(&b, msg)
	writeTextBody(&b, propString(msg.Props, mapi.PrBody))
	return b.Bytes(), nil
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
}

// writeTextBody appends the content headers, the header/body separator, and the
// text/plain body, transfer-encoded. 7-bit-clean text is emitted verbatim;
// anything else is quoted-printable.
func writeTextBody(b *bytes.Buffer, body string) {
	writeField(b, "Content-Type", "text/plain; charset=utf-8")
	if is7bitClean(body) {
		writeField(b, "Content-Transfer-Encoding", "7bit")
		b.WriteString("\r\n")
		b.WriteString(body)
		return
	}
	writeField(b, "Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(b)
	qp.Write([]byte(body))
	qp.Close()
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

// is7bitClean reports whether s is safe to emit as 7bit: only printable ASCII
// plus tab, CR, and LF.
func is7bitClean(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
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
