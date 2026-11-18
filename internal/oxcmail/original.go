package oxcmail

import (
	"html"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mime"
)

// originalSeparator opens the quoted original below a reply or forward body.
const originalSeparator = "-----Original Message-----"

// AppendOriginal adds the original message below the text of a reply or forward
// the client composed without it: a header block naming its sender, send time,
// recipients and subject, then its body. The plain body gets the text form; an
// HTML body gets the block inside its own document, so the result stays one HTML
// document. A forward also carries the original's attachments; a reply does not.
func AppendOriginal(reply, orig *Message, forward bool) {
	header := originalHeader(orig.Props)
	appendPlainOriginal(reply, orig, header)
	if _, ok := bytesProp(reply.Props, mapi.PrHTML); ok {
		appendHTMLOriginal(reply, orig, header)
	}
	if forward {
		reply.Attachments = append(reply.Attachments, orig.Attachments...)
	}
}

// AttachOriginal adds the original as an embedded message attachment (method 5,
// its RFC 5322 bytes in PR_ATTACH_DATA_BIN), the form a forward of an
// appointment takes ([MS-ASCMD] 2.2.1.19).
func AttachOriginal(fwd, orig *Message) error {
	raw, err := Export(orig, Options{})
	if err != nil {
		return err
	}
	name := propString(orig.Props, mapi.PrSubject)
	if name == "" {
		name = "original"
	}
	var a mapi.PropertyValues
	a.Set(mapi.PrAttachMethod, int32(mapi.AttachEmbeddedMsg))
	a.Set(mapi.PrAttachMimeTag, "message/rfc822")
	a.Set(mapi.PrAttachLongFilename, name+".eml")
	a.Set(mapi.PrAttachDataBin, raw)
	fwd.Attachments = append(fwd.Attachments, Attachment{Props: a})
	return nil
}

// originalHeader lists the header lines of the quoted original, skipping the ones
// the original does not carry.
func originalHeader(p mapi.PropertyValues) []string {
	from := propString(p, mapi.PrSentRepresentingName)
	if from == "" {
		from = propString(p, mapi.PrSenderName)
	}
	if addr := propString(p, mapi.PrSentRepresentingEmailAddress); addr != "" && addr != from {
		from = strings.TrimSpace(from + " <" + addr + ">")
	}
	sent := ""
	if v, ok := propUint64(p, mapi.PrClientSubmitTime); ok {
		sent = mapi.NTTimeToUnix(v).UTC().Format(time.RFC1123Z)
	}
	var lines []string
	for _, f := range [][2]string{
		{"From", from}, {"Sent", sent},
		{"To", propString(p, mapi.PrDisplayTo)}, {"Cc", propString(p, mapi.PrDisplayCc)},
		{"Subject", propString(p, mapi.PrSubject)},
	} {
		if f[1] != "" {
			lines = append(lines, f[0]+": "+f[1])
		}
	}
	return lines
}

// originalPlain is the original's body as text, derived from its HTML when it
// carries no plain body.
func originalPlain(p mapi.PropertyValues) string {
	if text := propString(p, mapi.PrBody); text != "" {
		return text
	}
	return PlainBodyFromHTML(p)
}

func appendPlainOriginal(reply, orig *Message, header []string) {
	var b strings.Builder
	b.WriteString(propString(reply.Props, mapi.PrBody))
	b.WriteString("\r\n\r\n" + originalSeparator + "\r\n")
	b.WriteString(strings.Join(header, "\r\n"))
	b.WriteString("\r\n\r\n")
	b.WriteString(originalPlain(orig.Props))
	reply.Props.Set(mapi.PrBody, b.String())
}

// appendHTMLOriginal inserts the quoted original before the reply document's
// closing body tag (or at its end when it has none), transcoding the reply to
// UTF-8 so both halves share one charset.
func appendHTMLOriginal(reply, orig *Message, header []string) {
	raw, _ := bytesProp(reply.Props, mapi.PrHTML)
	doc := mime.DecodeCharset(raw, htmlCharset(reply.Props))
	var b strings.Builder
	b.WriteString("<hr><div>")
	for _, line := range header {
		b.WriteString(html.EscapeString(line))
		b.WriteString("<br>")
	}
	b.WriteString("</div><br>")
	b.WriteString(originalHTMLBody(orig.Props))
	block := b.String()
	if i := strings.LastIndex(strings.ToLower(doc), "</body>"); i >= 0 {
		doc = doc[:i] + block + doc[i:]
	} else {
		doc += block
	}
	reply.Props.Set(mapi.PrHTML, []byte(doc))
	reply.Props.Set(mapi.PrInternetCodepage, int32(cpUTF8))
}

// originalHTMLBody is the content of the original's HTML body element, or its
// text escaped into a preformatted block when it has no HTML body.
func originalHTMLBody(p mapi.PropertyValues) string {
	raw, ok := bytesProp(p, mapi.PrHTML)
	if !ok {
		return "<pre>" + html.EscapeString(originalPlain(p)) + "</pre>"
	}
	doc := mime.DecodeCharset(raw, htmlCharset(p))
	lower := strings.ToLower(doc)
	if open := strings.Index(lower, "<body"); open >= 0 {
		if gt := strings.Index(lower[open:], ">"); gt >= 0 {
			doc, lower = doc[open+gt+1:], lower[open+gt+1:]
		}
	}
	if end := strings.LastIndex(lower, "</body>"); end >= 0 {
		doc = doc[:end]
	}
	return doc
}
