package mta

import (
	"bytes"
	"mime"
	"mime/quotedprintable"
	"time"
)

// Bounce builds a non-delivery report telling sender that their message could
// not be delivered to failed, with reason. It is a plain-text RFC 5322 message
// from the local mail system, marked auto-generated (RFC 3834) so its arrival in
// the sender's mailbox triggers no auto-reply and cannot loop — and its
// mailer-daemon origin is a role mailbox the auto-reply pass already skips.
//
// The outbound relay calls this when it abandons a recipient, then files the
// result into the sender's mailbox through the local delivery path, so a user
// whose external mail fails learns of it rather than the failure being silent.
func Bounce(sender, failed, reason string, when time.Time) []byte {
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
	writeReplyField(&b, "Content-Type", "text/plain; charset=utf-8")
	writeReplyField(&b, "Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	body := "Your message could not be delivered to one or more recipients.\r\n\r\n" +
		"    " + failed + "\r\n\r\n" +
		"The mail system reported:\r\n\r\n" +
		"    " + reason + "\r\n"
	qp := quotedprintable.NewWriter(&b)
	qp.Write([]byte(body))
	qp.Close()
	return b.Bytes()
}
