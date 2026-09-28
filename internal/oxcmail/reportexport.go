package oxcmail

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/textproto"
	"slices"
	"strconv"
	"strings"

	"hermex/internal/mapi"
)

// Export renders a REPORT.* message as the multipart/report it stands for
// (RFC 6522), so an internet client recognizes a delivery or disposition report:
// the readable body, then the machine-readable status part, then the returned
// message or headers.

// Report types, the report-type parameter and the status part's subtype.
const (
	reportDeliveryStatus = "delivery-status"
	reportDisposition    = "disposition-notification"
)

// reportKind returns the report type a message class names, "" for any other
// message.
func reportKind(msg *Message) string {
	class := strings.ToUpper(propString(msg.Props, mapi.PrMessageClass))
	if !strings.HasPrefix(class, "REPORT.") {
		return ""
	}
	switch {
	case strings.HasSuffix(class, ".IPNRN"), strings.HasSuffix(class, ".IPNNRN"):
		return reportDisposition
	case strings.HasSuffix(class, ".NDR"), strings.HasSuffix(class, ".DR"):
		return reportDeliveryStatus
	}
	return ""
}

// writeReportBody writes the multipart/report body of a report message. A status
// part the message carries as an attachment (an imported report keeps the one it
// arrived with) is written as it is; otherwise one is built from the report
// properties.
func writeReportBody(b *bytes.Buffer, msg *Message, opt Options, kind string) error {
	innerHdr, innerBytes, err := renderBody(msg, opt)
	if err != nil {
		return err
	}
	status, rest := splitStatusAttachment(msg.Attachments, kind)
	if status == nil {
		status = buildStatus(msg, kind)
	}
	var parts bytes.Buffer
	mw := multipart.NewWriter(&parts)
	if err := writePart(mw, innerHdr, innerBytes); err != nil {
		return err
	}
	sh := textproto.MIMEHeader{}
	sh.Set("Content-Type", "message/"+kind)
	if err := writePart(mw, sh, status); err != nil {
		return err
	}
	for _, att := range rest {
		if err := writeAttachmentPart(mw, att); err != nil {
			return err
		}
	}
	if err := mw.Close(); err != nil {
		return err
	}
	writeField(b, "Content-Type", "multipart/report; report-type="+kind+"; boundary=\""+mw.Boundary()+"\"")
	b.WriteString("\r\n")
	b.Write(parts.Bytes())
	return nil
}

// writePart writes one part into an open multipart writer.
func writePart(mw *multipart.Writer, h textproto.MIMEHeader, data []byte) error {
	w, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// splitStatusAttachment separates the attachment holding the report's status
// part, when there is one, from the other attachments.
func splitStatusAttachment(atts []Attachment, kind string) ([]byte, []Attachment) {
	var status []byte
	rest := make([]Attachment, 0, len(atts))
	for _, att := range atts {
		data, ok := bytesProp(att.Props, mapi.PrAttachDataBin)
		if status == nil && ok && strings.EqualFold(propString(att.Props, mapi.PrAttachMimeTag), "message/"+kind) {
			status = data
			continue
		}
		rest = append(rest, att)
	}
	return status, rest
}

// buildStatus builds the status part from the report properties.
func buildStatus(msg *Message, kind string) []byte {
	var b bytes.Buffer
	if kind == reportDisposition {
		writeMDNFields(&b, msg)
		return b.Bytes()
	}
	writeDSNFields(&b, msg)
	return b.Bytes()
}

// writeMDNFields writes the disposition fields of [MS-OXCMAIL] 2.1.3.6.2.
func writeMDNFields(b *bytes.Buffer, msg *Message) {
	t := senderTags
	if propString(msg.Props, t.smtp) == "" {
		t = representingTags
	}
	addr := propString(msg.Props, t.smtp)
	writeField(b, "Final-Recipient", "rfc822; "+addr)
	action := "displayed"
	if strings.HasSuffix(strings.ToUpper(propString(msg.Props, mapi.PrMessageClass)), ".IPNNRN") {
		action = "deleted"
	}
	writeField(b, "Disposition", "automatic-action/MDN-sent-automatically; "+action)
	if key, ok := bytesProp(msg.Props, mapi.PrParentKey); ok && len(key) > 0 {
		writeField(b, "X-MSExch-Correlation-Key", base64.StdEncoding.EncodeToString(key))
	}
	if id := propString(msg.Props, mapi.PrOriginalMessageID); id != "" {
		writeField(b, "Original-Message-ID", id)
	}
	if name := propString(msg.Props, t.name); name != "" && !strings.EqualFold(name, addr) {
		writeField(b, "X-Display-Name", encodeText(name))
	}
}

// dsnAction maps a delivery report class suffix back to its RFC 3464 Action.
var dsnAction = []struct{ suffix, action string }{
	{".EXPANDED.DR", "expanded"}, {".RELAYED.DR", "relayed"},
	{".DELAYED.DR", "delayed"}, {".NDR", "failed"}, {".DR", "delivered"},
}

// reportAction returns the Action a delivery report's class stands for.
func reportAction(msg *Message) string {
	class := strings.ToUpper(propString(msg.Props, mapi.PrMessageClass))
	for _, a := range dsnAction {
		if strings.HasSuffix(class, a.suffix) {
			return a.action
		}
	}
	return "failed"
}

// writeDSNFields writes the RFC 3464 per-message block, then one block for each
// recipient the report reports on.
func writeDSNFields(b *bytes.Buffer, msg *Message) {
	mta := propString(msg.Props, mapi.PrReportingMessageTransferAgent)
	if mta == "" {
		mta = "localhost"
	}
	if !strings.Contains(mta, ";") {
		mta = "dns; " + mta
	}
	writeField(b, "Reporting-MTA", mta)
	action := reportAction(msg)
	for _, r := range msg.Recipients {
		if !isReportedRecipient(r) {
			continue
		}
		b.WriteString("\r\n")
		writeDSNRecipient(b, r, action)
	}
}

// writeDSNRecipient writes one per-recipient block.
func writeDSNRecipient(b *bytes.Buffer, r mapi.PropertyValues, action string) {
	addr := propString(r, mapi.PrSmtpAddress)
	if addr == "" {
		addr = propString(r, mapi.PrEmailAddress)
	}
	writeField(b, "Final-Recipient", "rfc822; "+addr)
	writeField(b, "Action", action)
	writeField(b, "Status", recipientStatus(r, action))
	if mta := propString(r, mapi.PrRemoteMessageTransferAgent); mta != "" {
		writeField(b, "Remote-MTA", "dns; "+mta)
	}
	if info := propString(r, mapi.PrSupplementaryInfo); info != "" {
		writeField(b, "X-Supplementary-Info", info)
	}
}

// recipientStatus renders a recipient's stored status code as "K.S.D", or the
// generic status of the Action when none is stored.
func recipientStatus(r mapi.PropertyValues, action string) string {
	if code, ok := propInt32(r, mapi.PrNonDeliveryReportStatusCode); ok && code >= 100 && code <= 999 {
		return strconv.Itoa(int(code/100)) + "." + strconv.Itoa(int(code/10%10)) + "." + strconv.Itoa(int(code%10))
	}
	switch action {
	case "failed":
		return "5.0.0"
	case "delayed":
		return "4.0.0"
	}
	return "2.0.0"
}

// reportedRecipientTags are the properties a report puts on a recipient it
// reports on and never on an addressee.
var reportedRecipientTags = []mapi.PropTag{
	mapi.PrReportTime, mapi.PrDeliverTime, mapi.PrNonDeliveryReportStatusCode,
	mapi.PrSupplementaryInfo, mapi.PrRemoteMessageTransferAgent,
}

// isReportedRecipient reports whether a recipient row is one a delivery report
// reports on. Such a row is not an addressee of the report, so the address
// headers leave it out.
func isReportedRecipient(r mapi.PropertyValues) bool {
	return slices.ContainsFunc(reportedRecipientTags, r.Has)
}
