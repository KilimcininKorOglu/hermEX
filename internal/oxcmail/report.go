package oxcmail

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/mime"
)

// A delivery status notification (RFC 3464) and a message disposition
// notification (RFC 8098) arrive as multipart/report. [MS-OXCMAIL] 2.2.3.7 reads
// the machine-readable part into a REPORT.* message class and the report
// properties of [MS-OXOMSG], which is what makes Outlook show the message as a
// report rather than as ordinary mail.

// maxReportBlocks bounds how many field blocks of one status part are read. The
// part is sender-controlled, and each per-recipient block becomes a recipient row.
const maxReportBlocks = 500

// dsnSeverity ranks the RFC 3464 Action values; the most severe names the class.
var dsnSeverity = map[string]int{"delivered": 0, "expanded": 1, "relayed": 2, "delayed": 3, "failed": 4}

// dsnClassSuffix is the message class suffix each Action value selects.
var dsnClassSuffix = map[string]string{
	"delivered": ".DR", "expanded": ".Expanded.DR", "relayed": ".Relayed.DR",
	"delayed": ".Delayed.DR", "failed": ".NDR",
}

// mdnClassSuffix is the message class suffix each disposition-type selects.
var mdnClassSuffix = map[string]string{
	"displayed": ".IPNRN", "dispatched": ".IPNRN", "processed": ".IPNRN",
	"deleted": ".IPNNRN", "denied": ".IPNNRN", "failed": ".IPNNRN",
}

// importReport reads the status part of a multipart/report message, when the
// message is one, onto the message and its recipient table.
func importReport(root *mime.Part, msg *Message, stamp uint64) {
	if root.Type != "multipart" || root.Subtype != "report" {
		return
	}
	for _, child := range root.Children {
		if child.Type != "message" {
			continue
		}
		switch child.Subtype {
		case "delivery-status":
			importDSN(reportBlocks(child), msg, stamp)
			return
		case "disposition-notification":
			importMDN(reportBlocks(child), msg, stamp)
			return
		}
	}
}

// reportBlocks splits a status part into its field blocks, which blank lines
// separate: the per-message block first, then one block per recipient.
func reportBlocks(part *mime.Part) []textproto.MIMEHeader {
	data, err := part.DecodedContent()
	if err != nil {
		return nil
	}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	var blocks []textproto.MIMEHeader
	for chunk := range bytes.SplitSeq(data, []byte("\n\n")) {
		if len(bytes.TrimSpace(chunk)) == 0 {
			continue
		}
		if len(blocks) == maxReportBlocks {
			break
		}
		r := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(bytes.TrimLeft(chunk, "\n"), "\n\n"...))))
		if h, err := r.ReadMIMEHeader(); err == nil || len(h) > 0 {
			blocks = append(blocks, h)
		}
	}
	return blocks
}

// importDSN applies [MS-OXCMAIL] 2.2.3.7.1: the most severe Action names the
// class, and every recipient block of that Action becomes a recipient row.
func importDSN(blocks []textproto.MIMEHeader, msg *Message, stamp uint64) {
	if len(blocks) < 2 {
		return
	}
	action := mostSevereAction(blocks[1:])
	if action == "" {
		return
	}
	setReportClass(msg, dsnClassSuffix[action])
	if mta := strings.TrimSpace(blocks[0].Get("Reporting-MTA")); mta != "" {
		msg.Props.Set(mapi.PrReportingMessageTransferAgent, mta)
	}
	for _, b := range blocks[1:] {
		if strings.EqualFold(strings.TrimSpace(b.Get("Action")), action) {
			if r, ok := dsnRecipient(b, action, stamp); ok {
				msg.Recipients = append(msg.Recipients, r)
			}
		}
	}
}

// mostSevereAction returns the known Action of highest severity among the
// per-recipient blocks, "" when none carries a known one.
func mostSevereAction(blocks []textproto.MIMEHeader) string {
	action := ""
	for _, b := range blocks {
		a := strings.ToLower(strings.TrimSpace(b.Get("Action")))
		if sev, ok := dsnSeverity[a]; ok && (action == "" || sev > dsnSeverity[action]) {
			action = a
		}
	}
	return action
}

// dsnRecipient builds the recipient row one per-recipient block reports on.
func dsnRecipient(b textproto.MIMEHeader, action string, stamp uint64) (mapi.PropertyValues, bool) {
	name, addr := reportAddress(b.Get("Final-Recipient"))
	if addr == "" {
		name, addr = reportAddress(b.Get("Original-Recipient"))
	}
	if addr == "" {
		return nil, false
	}
	r := reportRecipientRow(name, addr)
	remote := typedValue(b.Get("Remote-MTA"))
	if remote != "" {
		r.Set(mapi.PrRemoteMessageTransferAgent, remote)
	}
	status := statusCode(b.Get("Status"))
	info := strings.TrimSpace(b.Get("X-Supplementary-Info"))
	if info == "" {
		info = supplementaryInfo(remote, status, strings.TrimSpace(b.Get("Diagnostic-Code")))
	}
	r.Set(mapi.PrSupplementaryInfo, info)
	r.Set(mapi.PrReportTime, stamp)
	if action != "failed" {
		r.Set(mapi.PrDeliverTime, stamp)
		return r, true
	}
	setStatusCodes(&r, status)
	return r, true
}

// reportRecipientRow is the SMTP recipient row of a reported address.
func reportRecipientRow(name, addr string) mapi.PropertyValues {
	if name == "" {
		name = addr
	}
	r := mapi.PropertyValues{}
	r.Set(mapi.PrDisplayName, name)
	r.Set(mapi.PrTransmitableDisplayName, name)
	r.Set(mapi.PrAddrType, "SMTP")
	r.Set(mapi.PrEmailAddress, addr)
	r.Set(mapi.PrSmtpAddress, addr)
	r.Set(mapi.PrSearchKey, addressSearchKey(addr))
	r.Set(mapi.PrObjectType, int32(mapi.ObjectTypeMailUser))
	r.Set(mapi.PrDisplayType, int32(mapi.DisplayTypeMailUser))
	r.Set(mapi.PrRecipientType, int32(mapi.RecipTo))
	return r
}

// supplementaryInfo builds the PidTagSupplementaryInfo value of [MS-OXCMAIL]
// 2.2.3.7.1.2: "<" [remote-mta SP] "#" status [SP diagnostic-code] ">".
func supplementaryInfo(remote, status, diag string) string {
	v := "#" + status
	if remote != "" {
		v = remote + " " + v
	}
	if diag != "" {
		v += " " + diag
	}
	return "<" + v + ">"
}

// statusCode returns the status-code portion of a Status field ("5.1.1").
func statusCode(field string) string {
	f := strings.Fields(field)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// setStatusCodes records a failed recipient's status per [MS-OXCMAIL] 2.2.3.7.1.3:
// the status as one number, and the diagnostic and reason codes it maps to.
func setStatusCodes(r *mapi.PropertyValues, status string) {
	kind, subject, detail := splitStatus(status)
	r.Set(mapi.PrNonDeliveryReportStatusCode, int32(kind*100+subject*10+detail))
	diag, reason := ndrCodes(subject, detail)
	r.Set(mapi.PrNonDeliveryReportDiagCode, diag)
	r.Set(mapi.PrNonDeliveryReportReasonCode, reason)
}

// splitStatus splits "K.S.D" into its three numbers; a subject or detail outside
// 0..9 reads as 0.
func splitStatus(status string) (kind, subject, detail int) {
	parts := strings.SplitN(status, ".", 3)
	nums := [3]int{}
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err == nil && n >= 0 && n <= 9 {
			nums[i] = n
		}
	}
	return nums[0], nums[1], nums[2]
}

// ndrDiag is the [MS-OXCMAIL] 2.2.3.7.1.3 table: for each Subject value, the
// DiagnosticCode each Detail value selects and the one any other Detail selects.
// A missing entry leaves DiagnosticCode at -1.
var ndrDiag = map[int]struct {
	byDetail map[int]int32
	other    int32
}{
	1: {map[int]int32{1: 35, 2: 48, 3: 32, 4: 1, 6: 40}, 0},
	2: {map[int]int32{1: 38, 2: 13, 3: 13, 4: 30}, 38},
	3: {map[int]int32{1: 38, 2: -1, 3: 18, 4: 13, 5: 18}, 38},
	4: {map[int]int32{0: -1, 3: -1, 4: -1, 6: 3, 7: 5, 8: 3}, 2},
	5: {map[int]int32{3: 16, 4: 11}, 17},
	6: {map[int]int32{2: 9, 3: 8, 4: 25, 5: -1}, 15},
	7: {map[int]int32{1: 29, 2: 28, 3: 26}, 46},
}

// ndrCodes returns the DiagnosticCode and ReasonCode a status subject and detail
// map to.
func ndrCodes(subject, detail int) (diag, reason int32) {
	diag = -1
	if row, ok := ndrDiag[subject]; ok {
		diag = row.other
		if d, ok := row.byDetail[detail]; ok {
			diag = d
		}
	}
	return diag, ndrReason[[2]int{subject, detail}]
}

// ndrReason is the ReasonCode the table sets; every other status leaves it 0.
var ndrReason = map[[2]int]int32{{1, 1}: 1, {4, 3}: 6, {6, 5}: 2}

// importMDN applies [MS-OXCMAIL] 2.2.3.7.2 to a disposition notification.
func importMDN(blocks []textproto.MIMEHeader, msg *Message, stamp uint64) {
	if len(blocks) == 0 {
		return
	}
	b := blocks[0]
	disposition := strings.TrimSpace(b.Get("Disposition"))
	suffix, ok := mdnClassSuffix[dispositionType(disposition)]
	if !ok {
		return
	}
	field := b.Get("Original-Recipient")
	if _, addr := reportAddress(field); addr == "" {
		field = b.Get("Final-Recipient")
	}
	if name, addr := reportAddress(field); addr != "" {
		msg.Props.Set(mapi.PrOriginalDisplayTo, firstNonEmpty(name, addr))
		if !msg.Props.Has(mapi.PrSentRepresentingSmtpAddress) && !msg.Props.Has(mapi.PrSenderSmtpAddress) {
			parseAddress((&mail.Address{Name: name, Address: addr}).String(), representingTags, &msg.Props)
			fillSenderRepresenting(msg)
		}
	}
	msg.Props.Set(mapi.PrOriginalDeliveryTime, stamp)
	msg.Props.Set(mapi.PrReceiptTime, stamp)
	msg.Props.Set(mapi.PrReportTime, stamp)
	msg.Props.Set(mapi.PrReportText, disposition)
	if key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b.Get("X-MSExch-Correlation-Key"))); err == nil && len(key) > 0 {
		msg.Props.Set(mapi.PrParentKey, key)
	}
	if id := strings.TrimSpace(b.Get("Original-Message-ID")); id != "" {
		msg.Props.Set(mapi.PrOriginalMessageID, id)
		msg.Props.Set(mapi.PrInternetReferences, id)
	}
	setReportClass(msg, suffix)
}

// dispositionType returns the disposition-type of a Disposition field:
// "automatic-action/MDN-sent-automatically; displayed/..." yields "displayed".
func dispositionType(field string) string {
	_, after, ok := strings.Cut(field, ";")
	if !ok {
		return ""
	}
	t, _, _ := strings.Cut(strings.TrimSpace(after), "/")
	return strings.ToLower(strings.TrimSpace(t))
}

// setReportClass turns the class the message had into its report class.
func setReportClass(msg *Message, suffix string) {
	class := propString(msg.Props, mapi.PrMessageClass)
	if class == "" {
		class = "IPM.Note"
	}
	msg.Props.Set(mapi.PrMessageClass, "REPORT."+class+suffix)
}

// reportAddress parses an address-type field ("rfc822; Name <addr>") into its
// display name and address.
func reportAddress(field string) (name, addr string) {
	v := typedValue(field)
	if v == "" {
		return "", ""
	}
	if a, err := mail.ParseAddress(v); err == nil {
		return a.Name, a.Address
	}
	if strings.ContainsAny(v, " <>\r\n") || !strings.Contains(v, "@") {
		return "", ""
	}
	return "", v
}

// typedValue drops the type label of a typed field ("dns; mx.example.org" yields
// "mx.example.org"); a field without one is returned trimmed.
func typedValue(field string) string {
	if _, after, ok := strings.Cut(field, ";"); ok {
		return strings.TrimSpace(after)
	}
	return strings.TrimSpace(field)
}

// firstNonEmpty returns a when it is set, b otherwise.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
