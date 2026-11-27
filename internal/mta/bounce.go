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
// auto-reply and cannot loop, and its mailer-daemon origin is a role mailbox the
// auto-reply pass already skips.
//
// The outbound relay calls this when it abandons a recipient, then files the
// result into the sender's mailbox through the local delivery path, so a user
// whose external mail fails learns of it rather than the failure being silent.
func Bounce(reportingMTA, sender, failed, reason string, when time.Time) ([]byte, error) {
	var parts bytes.Buffer
	mw := multipart.NewWriter(&parts)
	if err := writeReportPart(mw, "text/plain; charset=utf-8", []byte(bounceText(failed, reason))); err != nil {
		return nil, err
	}
	if err := writeReportPart(mw, "message/delivery-status", []byte(deliveryStatus(reportingMTA, failed, reason, when))); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	var b []byte
	daemon := "mailer-daemon@" + domainOf(sender)
	b = appendReplyField(b, "From", "Mail Delivery System <"+daemon+">")
	b = appendReplyField(b, "To", sender)
	b = appendReplyField(b, "Subject", mime.QEncoding.Encode("utf-8", "Undelivered Mail Returned to Sender"))
	b = appendReplyField(b, "Date", when.UTC().Format(dateLayout))
	b = appendReplyField(b, "Message-ID", "<"+newToken()+"@"+domainOf(sender)+">")
	// RFC 3834 §5: a bounce is auto-generated; the marker stops downstream
	// responders (including our own auto-reply) from answering it.
	b = appendReplyField(b, "Auto-Submitted", "auto-generated")
	b = appendReplyField(b, "MIME-Version", "1.0")
	b = appendReplyField(b, "Content-Type",
		`multipart/report; report-type="delivery-status"; boundary="`+mw.Boundary()+`"`)
	b = append(b, "\r\n"...)
	return append(b, parts.Bytes()...), nil
}

// writeReportPart writes one multipart/report body part with the given
// Content-Type and payload, propagating any part-creation or write error.
func writeReportPart(mw *multipart.Writer, contentType string, data []byte) error {
	p, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {contentType}})
	if err != nil {
		return err
	}
	_, err = p.Write(data)
	return err
}

// bounceText is the human-readable part of the report.
func bounceText(failed, reason string) string {
	return fmt.Sprintf(
		"Your message could not be delivered to one or more recipients.\r\n\r\n"+
			"    %s\r\n\r\n"+
			"The mail system reported:\r\n\r\n"+
			"    %s\r\n",
		failed, reason,
	)
}

// deliveryStatus is the machine-readable message/delivery-status part (RFC 3464):
// a per-message block (the reporting MTA, "dns;<host>", and the arrival date) then
// a per-recipient block naming the failed address, the permanent-failure action
// and status, and the SMTP diagnostic. The Reporting-MTA/Arrival-Date/
// Final-Recipient "rfc822;"/Action:failed/Status 5.0.0 fields match the reference
// MDA bounce; Diagnostic-Code (the remote response) and Last-Attempt-Date are
// added, both RFC 3464 standard. Remote-MTA is omitted: the reference reports its
// own host there for a local-delivery bounce, which would misattribute a relay
// failure to this server, and the failed exchanger's name is not threaded here.
func deliveryStatus(reportingMTA, failed, reason string, when time.Time) string {
	if reportingMTA == "" {
		reportingMTA = "localhost"
	}
	date := when.UTC().Format(dateLayout)
	return fmt.Sprintf(
		"Reporting-MTA: dns;%s\r\n"+
			"Arrival-Date: %s\r\n"+
			"\r\n"+ // blank line separates the per-message and per-recipient blocks
			"Final-Recipient: rfc822;%s\r\n"+
			"Action: failed\r\n"+
			"Status: 5.0.0\r\n"+
			"Diagnostic-Code: smtp; %s\r\n"+
			"Last-Attempt-Date: %s\r\n",
		reportingMTA, date, failed, oneLine(reason), date,
	)
}

// oneLine flattens whitespace runs (including CR/LF) to single spaces so an
// untrusted diagnostic string cannot inject extra delivery-status header fields.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
