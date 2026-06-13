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

	bodyText := "gövde içeriği — ünïçödé"
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
	eid, err := s.CreateMessage(mapi.PrivateFIDInbox, msg)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.OpenMessage(eid)
	if err != nil {
		t.Fatal(err)
	}

	gp := asMap(got.Props)
	if gp[mapi.PrSubject] != "açık konu" {
		t.Errorf("subject = %v", gp[mapi.PrSubject])
	}
	if gp[mapi.PrBody] != bodyText {
		t.Error("body did not reload from its content file")
	}

	if len(got.Recipients) != 2 {
		t.Fatalf("recipients = %d, want 2", len(got.Recipients))
	}
	if asMap(got.Recipients[0])[mapi.PrSmtpAddress] != "to@example.test" {
		t.Error("recipient 0 lost or reordered")
	}
	if asMap(got.Recipients[1])[mapi.PrSmtpAddress] != "cc@example.test" {
		t.Error("recipient 1 lost or reordered")
	}

	if len(got.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(got.Attachments))
	}
	ap := asMap(got.Attachments[0].Props)
	if ap[mapi.PrAttachLongFilename] != "a.pdf" {
		t.Errorf("attachment filename = %v", ap[mapi.PrAttachLongFilename])
	}
	if data, ok := ap[mapi.PrAttachDataBin].([]byte); !ok || !bytes.Equal(data, attachData) {
		t.Error("attachment payload did not reload from its content file")
	}

	if _, err := s.OpenMessage(999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("OpenMessage(missing) err = %v, want ErrNotFound", err)
	}
}
