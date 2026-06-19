package mta

import (
	"bytes"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"time"

	"hermex/internal/directory"
)

// readReceiptSubject is the fixed subject of a read-receipt MDN, matching the
// reference's read-notification template.
const readReceiptSubject = "Your message has been read!"

// ReadReceiptInfo carries the fields a read-receipt MDN needs, extracted by the
// caller from the message being marked read. Reader is the mailbox owner who read
// the message (the MDN From and Final-Recipient); To is the destination — the
// original message's PR_SENT_REPRESENTING_SMTP_ADDRESS. The Orig* fields decorate
// the human-readable part and correlate the notification to the original message.
type ReadReceiptInfo struct {
	Reader      string
	To          string
	OrigFrom    string
	OrigSubject string
	OrigMsgID   string
	SubmitTime  time.Time
}

// SendReadReceipt builds an Exchange-style disposition-notification (MDN) for a
// just-read message and delivers it to the message's represented sender. It is
// best-effort like the out-of-office pass — the caller logs the error and never
// fails the read that triggered it.
//
// The MDN is hand-built rather than routed through oxcmail.Export for the same
// reason buildAutoReply is: Export emits a fixed single-part header set and
// cannot produce the multipart/report; report-type=disposition-notification
// structure by which a receiving client recognizes a message as a read receipt.
func SendReadReceipt(accounts directory.Accounts, info ReadReceiptInfo, when time.Time) error {
	raw := buildReadReceipt(info, when)
	if _, err := Deliver(accounts, info.Reader, []string{info.To}, raw, when); err != nil {
		return err
	}
	return nil
}

// buildReadReceipt assembles the multipart/report MDN: a human-readable
// text/plain part followed by a message/disposition-notification part, wrapped in
// the RFC 5322 envelope addressed from the reader to the represented sender. The
// loop guard is X-Auto-Response-Suppress: All — the header Exchange uses to stop
// an auto-responder replying to the receipt (distinct from buildAutoReply's
// Auto-Submitted, which the reference does not set on a receipt).
func buildReadReceipt(info ReadReceiptInfo, when time.Time) []byte {
	var parts bytes.Buffer
	mw := multipart.NewWriter(&parts)
	if p, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type": {"text/plain; charset=utf-8"},
	}); err == nil {
		p.Write([]byte(readReceiptText(info)))
	}
	if p, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type": {"message/disposition-notification"},
	}); err == nil {
		p.Write([]byte(dispositionNotification(info)))
	}
	mw.Close()

	var msg bytes.Buffer
	writeReplyField(&msg, "From", info.Reader)
	writeReplyField(&msg, "To", info.To)
	writeReplyField(&msg, "Subject", mime.QEncoding.Encode("utf-8", readReceiptSubject))
	writeReplyField(&msg, "Date", when.UTC().Format(dateLayout))
	writeReplyField(&msg, "Message-ID", "<"+newToken()+"@"+domainOf(info.Reader)+">")
	writeReplyField(&msg, "X-Auto-Response-Suppress", "All")
	writeReplyField(&msg, "MIME-Version", "1.0")
	writeReplyField(&msg, "Content-Type",
		`multipart/report; report-type="disposition-notification"; boundary="`+mw.Boundary()+`"`)
	msg.WriteString("\r\n")
	msg.Write(parts.Bytes())
	return msg.Bytes()
}

// readReceiptText is the human-readable part, modeled on the reference's
// read-notification template: it states the message was displayed to the reader
// and echoes the original sender and subject. The reference's optional recipient
// and length lines, which need the original recipient table and size, are omitted
// as non-load-bearing.
func readReceiptText(info ReadReceiptInfo) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Your message has been displayed to %q.\r\n\r\n", info.Reader)
	b.WriteString("Below is the original mail information:\r\n")
	if !info.SubmitTime.IsZero() {
		fmt.Fprintf(&b, "   Time:    %s\r\n", info.SubmitTime.UTC().Format(dateLayout))
	}
	if info.OrigFrom != "" {
		fmt.Fprintf(&b, "   From:    %s\r\n", info.OrigFrom)
	}
	if info.OrigSubject != "" {
		fmt.Fprintf(&b, "   Subject: %s\r\n", info.OrigSubject)
	}
	return b.String()
}

// dispositionNotification is the machine-readable message/disposition-notification
// part ([RFC 3798] / [MS-OXOMSG]): the reader is the Final-Recipient and the
// disposition is an automatically-sent "displayed" (read) notification.
func dispositionNotification(info ReadReceiptInfo) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Final-Recipient: rfc822;%s\r\n", info.Reader)
	b.WriteString("Disposition: automatic-action/MDN-sent-automatically; displayed\r\n")
	if info.OrigMsgID != "" {
		fmt.Fprintf(&b, "Original-Message-ID: %s\r\n", info.OrigMsgID)
	}
	return b.String()
}
