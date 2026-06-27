package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// TestRecipientTableRoundTrip confirms the inline recipient table the OpenMessage and
// ReloadCachedInformation responses emit decodes back to the same recipients through
// the wire decoder a client uses, so Outlook reads To/Cc/Bcc on a stored message.
func TestRecipientTableRoundTrip(t *testing.T) {
	recipients := []mapi.PropertyValues{
		{
			{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipTo)},
			{Tag: mapi.PrDisplayName, Value: "Alice"},
			{Tag: mapi.PrEmailAddress, Value: "alice@example.com"},
			{Tag: mapi.PrAddrType, Value: "SMTP"},
		},
		{
			{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipCc)},
			{Tag: mapi.PrDisplayName, Value: "Bob"},
			{Tag: mapi.PrSmtpAddress, Value: "bob@example.com"},
		},
	}
	out := ext.NewPush(ext.FlagUTF16)
	writeRecipientTable(out, recipients)

	p := ext.NewPull(out.Bytes(), ext.FlagUTF16)
	count, err := p.Uint16()
	if err != nil || count != 2 {
		t.Fatalf("RecipientCount = %d, err %v", count, err)
	}
	cols, err := p.PropTags()
	if err != nil {
		t.Fatalf("RecipientColumns: %v", err)
	}
	rowCount, err := p.Uint8()
	if err != nil || rowCount != 2 {
		t.Fatalf("RowCount = %d, err %v", rowCount, err)
	}

	wants := []struct {
		typ         uint8
		name, email string
	}{
		{uint8(mapi.RecipTo), "Alice", "alice@example.com"},
		{uint8(mapi.RecipCc), "Bob", "bob@example.com"},
	}
	for i, w := range wants {
		rcptType, _ := p.Uint8()
		if rcptType != w.typ {
			t.Errorf("row %d type = %d, want %d", i, rcptType, w.typ)
		}
		if _, err := p.Uint16(); err != nil { // CodePageId
			t.Fatalf("row %d cpid: %v", i, err)
		}
		if _, err := p.Uint16(); err != nil { // Reserved
			t.Fatalf("row %d reserved: %v", i, err)
		}
		bag, ok := pullRecipientRow(p, cols)
		if !ok {
			t.Fatalf("row %d: RecipientRow decode failed", i)
		}
		if got := stringProp(bag, mapi.PrDisplayName); got != w.name {
			t.Errorf("row %d name = %q, want %q", i, got, w.name)
		}
		if got := stringProp(bag, mapi.PrEmailAddress); got != w.email {
			t.Errorf("row %d email = %q, want %q", i, got, w.email)
		}
	}
}
