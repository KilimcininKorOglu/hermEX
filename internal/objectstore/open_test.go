package objectstore

import (
	"bytes"
	"errors"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// TestOpenMessage reconstructs a stored message and verifies the object model
// comes back faithfully: top-level properties (including the cid-offloaded
// body), recipients in insertion order, and the attachment with its offloaded
// payload. A missing message reports ErrNotFound.
func TestOpenMessage(t *testing.T) {
	s := openSeededStore(t)

	bodyText := "gövde içeriği, ünïçödé"
	attachData := []byte("PDF-DATA-bytes")
	msg := &oxcmail.Message{
		Props: mapi.PropertyValues{
			{Tag: mapi.PrSubject, Value: "açık konu"},
			{Tag: mapi.PrBody, Value: bodyText},
		},
		Recipients: []mapi.PropertyValues{
			{{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipTo)}, {Tag: mapi.PrSmtpAddress, Value: "to@example.test"}},
			{{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipCc)}, {Tag: mapi.PrSmtpAddress, Value: "cc@example.test"}},
		},
		Attachments: []oxcmail.Attachment{
			{Props: mapi.PropertyValues{
				{Tag: mapi.PrAttachLongFilename, Value: "a.pdf"},
				{Tag: mapi.PrAttachDataBin, Value: attachData},
			}},
		},
	}
	eid := mustCreateMessage(t, s, mapi.PrivateFIDInbox, msg)
	got := mustOpenMessage(t, s, eid)

	gp := asMap(got.Props)
	wantEq(t, "subject", gp[mapi.PrSubject], any("açık konu"))
	wantEq(t, "body reloaded from its content file", gp[mapi.PrBody], any(bodyText))

	if len(got.Recipients) != 2 {
		t.Fatalf("recipients = %d, want 2", len(got.Recipients))
	}
	wantEq(t, "recipient 0 (insertion order)", asMap(got.Recipients[0])[mapi.PrSmtpAddress], any("to@example.test"))
	wantEq(t, "recipient 1 (insertion order)", asMap(got.Recipients[1])[mapi.PrSmtpAddress], any("cc@example.test"))

	if len(got.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(got.Attachments))
	}
	ap := asMap(got.Attachments[0].Props)
	wantEq(t, "attachment filename", ap[mapi.PrAttachLongFilename], any("a.pdf"))
	data, _ := ap[mapi.PrAttachDataBin].([]byte)
	if !bytes.Equal(data, attachData) {
		t.Error("attachment payload did not reload from its content file")
	}

	if _, err := s.OpenMessage(999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("OpenMessage(missing) err = %v, want ErrNotFound", err)
	}
}
