package mta

import (
	"bytes"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
	"time"
)

// Bounce builds a non-delivery report (RFC 3464) telling sender that their
// message could not be delivered to failed, with reason. It is a
// multipart/report; report-type=delivery-status: a human-readable text/plain part
// plus a machine-readable message/delivery-status part that a mail client parses
// to surface the structured failure. reportingMTA is the host that gave up on the
// recipient (this server's announced hostname). The report is marked
// auto-generated (RFC 3834) so its arrival in the sender's mailbox triggers no
// auto-reply and cannot loop — and its mailer-daemon origin is a role mailbox the
// auto-reply pass already skips.
//
// The outbound relay calls this when it abandons a recipient, then files the
// result into the sender's mailbox through the local delivery path, so a user
// whose external mail fails learns of it rather than the failure being silent.
func Bounce(reportingMTA, sender, failed, reason string, when time.Time) []byte {
	var parts bytes.Buffer
	mw := multipart.NewWriter(&parts)
	if p, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type": {"text/plain; charset=utf-8"},
	}); err == nil {
		p.Write([]byte(bounceText(failed, reason)))
	}
	if p, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type": {"message/delivery-status"},
	}); err == nil {
		p.Write([]byte(deliveryStatus(reportingMTA, failed, reason, when)))
	}
	mw.Close()

	var b bytes.Buffer
	daemon := "mailer-daemon@" + domainOf(sender)
	writeReplyField(&b, "From", "Mail Delivery System <"+daemon+">")
	writeReplyField(&b, "To", sender)
	writeReplyField(&b, "Subject", mime.QEncoding.Encode("utf-8", "Undelivered Mail Returned to Sender"))
	writeReplyField(&b, "Date", when.UTC().Format(dateLayout))
	writeReplyField(&b, "Message-ID", "<"+newToken()+"@"+domainOf(sender)+">")
	// RFC 3834 §5: a bounce is auto-generated; the marker stops downstream
	// responders (including our own auto-reply) from answering it.
	writeReplyField(&b, "Auto-Submitted", "auto-generated")
	writeReplyField(&b, "MIME-Version", "1.0")
	writeReplyField(&b, "Content-Type",
		`multipart/report; report-type="delivery-status"; boundary="`+mw.Boundary()+`"`)
	b.WriteString("\r\n")
	b.Write(parts.Bytes())
	return b.Bytes()
}

// bounceText is the human-readable part of the report.
func bounceText(failed, reason string) string {
	var b bytes.Buffer
	b.WriteString("Your message could not be delivered to one or more recipients.\r\n\r\n")
	fmt.Fprintf(&b, "    %s\r\n\r\n", failed)
	b.WriteString("The mail system reported:\r\n\r\n")
	fmt.Fprintf(&b, "    %s\r\n", reason)
	return b.String()
}

// deliveryStatus is the machine-readable message/delivery-status part (RFC 3464):
// a per-message block (the reporting MTA, "dns;<host>", and the arrival date) then
// a per-recipient block naming the failed address, the permanent-failure action
// and status, and the SMTP diagnostic. The Reporting-MTA/Arrival-Date/
// Final-Recipient "rfc822;"/Action:failed/Status 5.0.0 fields match the reference
// MDA bounce; Diagnostic-Code (the remote response) and Last-Attempt-Date are
// added — both RFC 3464 standard. Remote-MTA is omitted: the reference reports its
// own host there for a local-delivery bounce, which would misattribute a relay
// failure to this server, and the failed exchanger's name is not threaded here.
func deliveryStatus(reportingMTA, failed, reason string, when time.Time) string {
	if reportingMTA == "" {
		reportingMTA = "localhost"
	}
	date := when.UTC().Format(dateLayout)
	var b bytes.Buffer
	fmt.Fprintf(&b, "Reporting-MTA: dns;%s\r\n", reportingMTA)
	fmt.Fprintf(&b, "Arrival-Date: %s\r\n", date)
	b.WriteString("\r\n") // blank line separates the per-message and per-recipient blocks
	fmt.Fprintf(&b, "Final-Recipient: rfc822;%s\r\n", failed)
	b.WriteString("Action: failed\r\n")
	b.WriteString("Status: 5.0.0\r\n")
	fmt.Fprintf(&b, "Diagnostic-Code: smtp; %s\r\n", oneLine(reason))
	fmt.Fprintf(&b, "Last-Attempt-Date: %s\r\n", date)
	return b.String()
}

// oneLine flattens whitespace runs (including CR/LF) to single spaces so an
// untrusted diagnostic string cannot inject extra delivery-status header fields.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
