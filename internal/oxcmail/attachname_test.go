package oxcmail

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// TestNonASCIIAttachmentNameIsEncoded proves a filename outside ASCII leaves as
// encoded header text and comes back unchanged. It used to be written as raw
// UTF-8 into both header parameters, which a strict reader shows as mojibake.
func TestNonASCIIAttachmentNameIsEncoded(t *testing.T) {
	const name = "Rapor şubat ğüİ.pdf"
	msg := &Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "x"},
		{Tag: mapi.PrSenderSmtpAddress, Value: "alice@hermex.test"},
		{Tag: mapi.PrBody, Value: "body"},
	}, Attachments: []Attachment{{Props: mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
		{Tag: mapi.PrAttachMimeTag, Value: "application/pdf"},
		{Tag: mapi.PrAttachLongFilename, Value: name},
		{Tag: mapi.PrAttachDataBin, Value: []byte("%PDF")},
	}}}}
	raw, err := Export(msg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := strings.Cut(string(raw), "%PDF")
	for i := 0; i < len(head); i++ {
		if head[i] >= 0x80 {
			t.Fatalf("8-bit byte in the headers:\n%s", raw)
		}
	}
	back, err := Import(raw, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Attachments) != 1 || propString(back.Attachments[0].Props, mapi.PrAttachLongFilename) != name {
		t.Errorf("round-tripped name = %+v", back.Attachments)
	}
}

// TestAttachmentDisplayNameTravelsAsContentDescription proves an attachment's
// display name is written as its Content-Description, encoded when it is not
// ASCII, and read back into the display name ([MS-OXCMAIL] 2.1.3.4.1).
func TestAttachmentDisplayNameTravelsAsContentDescription(t *testing.T) {
	const desc = "Şubat raporu"
	msg := &Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "x"},
		{Tag: mapi.PrSenderSmtpAddress, Value: "alice@hermex.test"},
		{Tag: mapi.PrBody, Value: "body"},
	}, Attachments: []Attachment{{Props: mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
		{Tag: mapi.PrAttachMimeTag, Value: "application/pdf"},
		{Tag: mapi.PrAttachLongFilename, Value: "r.pdf"},
		{Tag: mapi.PrDisplayName, Value: desc},
		{Tag: mapi.PrAttachDataBin, Value: []byte("%PDF")},
	}}}}
	raw, err := Export(msg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Content-Description: =?") {
		t.Fatalf("no encoded Content-Description:\n%s", raw)
	}
	back, err := Import(raw, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Attachments) != 1 || propString(back.Attachments[0].Props, mapi.PrDisplayName) != desc {
		t.Errorf("round-tripped display name = %+v", back.Attachments)
	}
}

// TestContentDescriptionNamesAnUnnamedAttachment proves an attachment part with
// no filename takes its name from a non-empty Content-Description, as the file
// name rule of [MS-OXCMAIL] 2.2.3.4.1 orders.
func TestContentDescriptionNamesAnUnnamedAttachment(t *testing.T) {
	raw := "From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: x\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nbody\r\n" +
		"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment\r\n" +
		"Content-Description: =?utf-8?q?Q3_=C3=B6zet.pdf?=\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERg==\r\n--b--\r\n"
	msg, err := Import([]byte(raw), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d", len(msg.Attachments))
	}
	if got := propString(msg.Attachments[0].Props, mapi.PrAttachLongFilename); got != "Q3 özet.pdf" {
		t.Errorf("filename = %q, want the Content-Description", got)
	}
}
