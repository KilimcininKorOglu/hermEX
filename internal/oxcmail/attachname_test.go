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
