package oxcmail

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// dsnMail builds a multipart/report delivery status notification around the given
// per-recipient blocks.
func dsnMail(recipients string) string {
	return "From: MAILER-DAEMON@mx.example.org\r\nTo: sender@example.org\r\nSubject: Undelivered Mail\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/report; report-type=delivery-status; boundary=\"r\"\r\n\r\n" +
		"--r\r\nContent-Type: text/plain\r\n\r\nYour message could not be delivered.\r\n" +
		"--r\r\nContent-Type: message/delivery-status\r\n\r\nReporting-MTA: dns; mx.example.org\r\n\r\n" + recipients +
		"--r\r\nContent-Type: text/rfc822-headers\r\n\r\nSubject: original\r\n\r\n--r--\r\n"
}

const (
	failedBlock = "Final-Recipient: rfc822; gone@example.net\r\nAction: failed\r\nStatus: 5.1.1\r\n" +
		"Remote-MTA: dns; mx.example.net\r\nDiagnostic-Code: smtp; 550 5.1.1 no such user\r\n\r\n"
	deliveredBlock = "Final-Recipient: rfc822; ok@example.net\r\nAction: delivered\r\nStatus: 2.0.0\r\n\r\n"
)

// reportedRow returns the recipient row reporting on addr.
func reportedRow(t *testing.T, msg *Message, addr string) mapi.PropertyValues {
	t.Helper()
	for _, r := range msg.Recipients {
		if propString(r, mapi.PrSmtpAddress) == addr && isReportedRecipient(r) {
			return r
		}
	}
	t.Fatalf("no reported recipient row for %s", addr)
	return nil
}

// TestImportNDR proves a failure report becomes a non-delivery report
// ([MS-OXCMAIL] 2.2.3.7.1): the most severe Action names the class, only the
// recipients of that Action are rows, and the status is recorded as the numbers
// Outlook reads.
func TestImportNDR(t *testing.T) {
	msg, err := Import([]byte(dsnMail(failedBlock+deliveredBlock)), Options{})
	mustImport(t, err)

	wantProp(t, "class", propString(msg.Props, mapi.PrMessageClass), "REPORT.IPM.Note.NDR")
	wantProp(t, "reporting MTA", propString(msg.Props, mapi.PrReportingMessageTransferAgent), "dns; mx.example.org")
	r := reportedRow(t, msg, "gone@example.net")
	wantProp(t, "remote MTA", propString(r, mapi.PrRemoteMessageTransferAgent), "mx.example.net")
	wantProp(t, "supplementary info", propString(r, mapi.PrSupplementaryInfo), "<mx.example.net #5.1.1 smtp; 550 5.1.1 no such user>")
	for tag, want := range map[mapi.PropTag]int32{
		mapi.PrNonDeliveryReportStatusCode: 511,
		mapi.PrNonDeliveryReportDiagCode:   35,
		mapi.PrNonDeliveryReportReasonCode: 1,
	} {
		if got, _ := propInt32(r, tag); got != want {
			t.Errorf("%v = %d, want %d", tag, got, want)
		}
	}
	if r.Has(mapi.PrDeliverTime) {
		t.Error("a failed recipient has no delivery time")
	}
	for _, row := range msg.Recipients {
		if propString(row, mapi.PrSmtpAddress) == "ok@example.net" {
			t.Error("a recipient of a less severe Action must be skipped")
		}
	}
}

// TestImportDeliveryReport proves a success report is a delivery report whose
// recipient carries the delivery time.
func TestImportDeliveryReport(t *testing.T) {
	msg, err := Import([]byte(dsnMail(deliveredBlock)), Options{})
	mustImport(t, err)

	wantProp(t, "class", propString(msg.Props, mapi.PrMessageClass), "REPORT.IPM.Note.DR")
	if r := reportedRow(t, msg, "ok@example.net"); !r.Has(mapi.PrDeliverTime) {
		t.Error("a delivered recipient carries PidTagDeliverTime")
	}
}

const mdnMail = "From: reader@example.net\r\nTo: sender@example.org\r\nSubject: Read: hello\r\n" +
	"MIME-Version: 1.0\r\nContent-Type: multipart/report; report-type=disposition-notification; boundary=\"m\"\r\n\r\n" +
	"--m\r\nContent-Type: text/plain\r\n\r\nYour message was read.\r\n" +
	"--m\r\nContent-Type: message/disposition-notification\r\n\r\n" +
	"Final-Recipient: rfc822; reader@example.net\r\nOriginal-Message-ID: <orig@example.org>\r\n" +
	"Disposition: manual-action/MDN-sent-manually; displayed\r\nX-MSExch-Correlation-Key: AQID\r\n\r\n--m--\r\n"

// TestImportMDN proves a read notification becomes a read report ([MS-OXCMAIL]
// 2.2.3.7.2) that names the reader and the message it answers.
func TestImportMDN(t *testing.T) {
	msg, err := Import([]byte(mdnMail), Options{})
	mustImport(t, err)

	wantProp(t, "class", propString(msg.Props, mapi.PrMessageClass), "REPORT.IPM.Note.IPNRN")
	wantProp(t, "report text", propString(msg.Props, mapi.PrReportText), "manual-action/MDN-sent-manually; displayed")
	wantProp(t, "original display to", propString(msg.Props, mapi.PrOriginalDisplayTo), "reader@example.net")
	wantProp(t, "original message id", propString(msg.Props, mapi.PrOriginalMessageID), "<orig@example.org>")
	if key, _ := bytesProp(msg.Props, mapi.PrParentKey); string(key) != "\x01\x02\x03" {
		t.Errorf("parent key = %x, want 010203", key)
	}
}

// TestExportReportIsMultipartReport proves a stored report goes out as the
// multipart/report it arrived as, with its status part intact and without the
// reported recipients in its address headers.
func TestExportReportIsMultipartReport(t *testing.T) {
	msg, err := Import([]byte(dsnMail(failedBlock)), Options{})
	mustImport(t, err)

	raw, err := Export(msg, Options{})
	mustImport(t, err)
	out := string(raw)
	if !strings.Contains(out, "Content-Type: multipart/report; report-type=delivery-status;") {
		t.Fatalf("a report is not exported as multipart/report:\n%s", out)
	}
	if !strings.Contains(out, "Content-Type: message/delivery-status\r\n\r\nReporting-MTA: dns; mx.example.org") {
		t.Errorf("the status part did not survive:\n%s", out)
	}
	if strings.Contains(out, "To: \"gone@example.net\"") || strings.Contains(out, "gone@example.net>") {
		t.Errorf("a reported recipient became an addressee:\n%s", out)
	}
}

// TestExportBuildsTheDispositionFields proves a read report a client creates
// (with no status part of its own) is sent with the disposition fields of
// [MS-OXCMAIL] 2.1.3.6.2.
func TestExportBuildsTheDispositionFields(t *testing.T) {
	msg := &Message{}
	msg.Props.Set(mapi.PrMessageClass, "REPORT.IPM.Note.IPNNRN")
	msg.Props.Set(mapi.PrSubject, "Not read: hello")
	msg.Props.Set(mapi.PrSenderSmtpAddress, "reader@example.net")
	msg.Props.Set(mapi.PrSenderName, "Reader")
	msg.Props.Set(mapi.PrOriginalMessageID, "<orig@example.org>")
	msg.Props.Set(mapi.PrBody, "Your message was deleted without being read.")

	raw, err := Export(msg, Options{})
	mustImport(t, err)
	out := string(raw)
	for _, want := range []string{
		"report-type=disposition-notification",
		"Final-Recipient: rfc822; reader@example.net\r\n",
		"Disposition: automatic-action/MDN-sent-automatically; deleted\r\n",
		"Original-Message-ID: <orig@example.org>\r\n",
		"X-Display-Name: Reader\r\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}
