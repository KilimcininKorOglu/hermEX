package oxcmail

import (
	"bytes"
	"net/mail"
	"testing"

	"hermex/internal/mapi"
)

// TestImportExportReadReceipt checks the read-receipt (MDN) round trip: a
// Disposition-Notification-To header imports to PR_READ_RECEIPT_REQUESTED plus
// the notification address, and export re-emits the header addressed to it.
func TestImportExportReadReceipt(t *testing.T) {
	raw := []byte("From: Bob <bob@example.com>\r\n" +
		"To: alice@example.com\r\n" +
		"Subject: please confirm\r\n" +
		"Disposition-Notification-To: Sender Name <bob@example.com>\r\n" +
		"\r\n" +
		"body\r\n")
	msg, err := Import(raw, Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if v, ok := msg.Props.Get(mapi.PrReadReceiptRequested); !ok || v != true {
		t.Errorf("PR_READ_RECEIPT_REQUESTED = %v (ok=%v); want true", v, ok)
	}
	if v, _ := msg.Props.Get(mapi.PrReadReceiptSmtpAddress); v != "bob@example.com" {
		t.Errorf("read-receipt smtp address = %v, want bob@example.com", v)
	}

	wire, err := Export(msg, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(wire))
	if err != nil {
		t.Fatalf("export not parseable: %v", err)
	}
	dnt, err := mail.ParseAddress(m.Header.Get("Disposition-Notification-To"))
	if err != nil {
		t.Fatalf("Disposition-Notification-To not emitted/parseable: %v\n%s", err, wire)
	}
	if dnt.Address != "bob@example.com" {
		t.Errorf("Disposition-Notification-To address = %q, want bob@example.com", dnt.Address)
	}
}

// TestExportNoReadReceiptWhenUnset checks that export emits no
// Disposition-Notification-To header unless a read receipt was requested.
func TestExportNoReadReceiptWhenUnset(t *testing.T) {
	msg, err := Import([]byte("From: bob@example.com\r\nTo: alice@example.com\r\nSubject: x\r\n\r\nhi\r\n"), Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	wire, err := Export(msg, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if bytes.Contains(wire, []byte("Disposition-Notification-To")) {
		t.Errorf("unexpected Disposition-Notification-To:\n%s", wire)
	}
}
