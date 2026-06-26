package mta

import (
	"bytes"
	"fmt"
	"mime"
	"strings"
	"time"

	"hermex/internal/directory"
)

// notifyQuarantine builds and locally delivers the quarantine notice to every
// distinct address in affected+adminEmails (the recipients for inbound mail, the
// sender for outbound, plus the resolved domain/org admins), one copy each. It
// uses Deliver, the local path scanning never hooks, so the notice is never
// itself scanned (no loop). Delivery is best-effort: a failed notice must not
// fail the scan path.
func notifyQuarantine(accounts directory.Accounts, rec directory.QuarantineRecord, affected, adminEmails []string, hostname string, when time.Time) {
	seen := map[string]bool{}
	var to []string
	for _, a := range append(append([]string{}, affected...), adminEmails...) {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		to = append(to, a)
	}
	for _, rcpt := range to {
		notice := buildQuarantineNotice(rec, rcpt, hostname, when)
		_, _ = Deliver(accounts, "", []string{rcpt}, notice, when)
	}
}

// buildQuarantineNotice composes the text-only Turkish notice for one recipient.
// It carries only the quarantine metadata (sender, subject, virus, file); the
// infected bytes are never attached or quoted, so the notice cannot re-trip the
// scanner. It is marked auto-generated (RFC 3834) to stop auto-replies.
func buildQuarantineNotice(rec directory.QuarantineRecord, to, hostname string, when time.Time) []byte {
	if hostname == "" {
		hostname = "localhost"
	}
	var b bytes.Buffer
	writeReplyField(&b, "From", "hermEX Antivirus <postmaster@"+hostname+">")
	writeReplyField(&b, "To", to)
	writeReplyField(&b, "Subject", mime.QEncoding.Encode("utf-8", "Karantina bildirimi: virüs tespit edildi"))
	writeReplyField(&b, "Date", when.UTC().Format(dateLayout))
	writeReplyField(&b, "Message-ID", "<"+newToken()+"@"+hostname+">")
	writeReplyField(&b, "Auto-Submitted", "auto-generated")
	writeReplyField(&b, "MIME-Version", "1.0")
	writeReplyField(&b, "Content-Type", "text/plain; charset=utf-8")
	b.WriteString("\r\n")
	b.WriteString(quarantineNoticeText(rec))
	return b.Bytes()
}

// quarantineNoticeText is the Turkish notice body. Untrusted fields (sender,
// subject, file) are flattened to a single line so they cannot disrupt the body.
func quarantineNoticeText(rec directory.QuarantineRecord) string {
	subject := oneLine(rec.Subject)
	if subject == "" {
		subject = "(konusuz)"
	}
	virus := oneLine(rec.VirusName)
	cause := virus + " virüsü"
	if f := oneLine(rec.InfectedFile); f != "" {
		cause = "'" + f + "' dosyasındaki " + virus + " virüsü"
	}
	var b bytes.Buffer
	if rec.Direction == "outbound" {
		fmt.Fprintf(&b, "Gönderdiğiniz '%s' konulu mail, %s sebebiyle gönderilmeyip karantinaya alındı.\r\n\r\n", subject, cause)
	} else {
		fmt.Fprintf(&b, "%s adresinden gelen '%s' konulu mail, %s sebebiyle teslim edilmeyip karantinaya alındı.\r\n\r\n",
			oneLine(rec.MailFrom), subject, cause)
	}
	b.WriteString("Lütfen yöneticinize danışınız.\r\n")
	return b.String()
}
