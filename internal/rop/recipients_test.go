package rop

import (
	"fmt"
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
	wantU16(t, p, "RecipientCount", 2)
	cols, err := p.PropTags()
	if err != nil {
		t.Fatalf("RecipientColumns: %v", err)
	}
	wantU8(t, p, "RowCount", 2)

	for i, w := range []struct {
		typ         uint8
		name, email string
	}{
		{uint8(mapi.RecipTo), "Alice", "alice@example.com"},
		{uint8(mapi.RecipCc), "Bob", "bob@example.com"},
	} {
		label := fmt.Sprintf("row %d", i)
		wantU8(t, p, label+" type", w.typ)
		mustU16(t, p, label+" CodePageId")
		mustU16(t, p, label+" Reserved")
		bag, ok := pullRecipientRow(p, cols)
		if !ok {
			t.Fatalf("%s: RecipientRow decode failed", label)
		}
		wantProp(t, bag, mapi.PrDisplayName, w.name, label+" name")
		wantProp(t, bag, mapi.PrEmailAddress, w.email, label+" email")
	}
}
