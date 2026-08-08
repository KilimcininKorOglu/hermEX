package objectstore

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
	"hermex/internal/smime"
)

// appendForEdit stores a plain message and reads it once, so the eml cache is
// populated and warm: every case below is about what an in-place edit does to an
// already-cached message, which is the state the fault needs.
func appendForEdit(t *testing.T, s *Store, subject string) MessageInfo {
	t.Helper()
	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: " + subject +
		"\r\nDate: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nbody text\r\n")
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.emlPath(midString(uint64(info.ID)))); err != nil {
		t.Fatalf("the message is not cached, so the test would not exercise the fault: %v", err)
	}
	return info
}

// indexSize reports the RFC822 size the index records for a message, the value IMAP
// answers RFC822.SIZE with and POP3 answers LIST with.
func indexSize(t *testing.T, s *Store, uid uint32) int64 {
	t.Helper()
	msgs, err := s.ListMessages(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.UID == uid {
			return m.Size
		}
	}
	t.Fatalf("message uid %d is not indexed", uid)
	return 0
}

// TestEditedSubjectReachesTheWire proves an in-place property edit is served: the
// cached wire form is rebuilt, so a reader gets the edited message rather than the
// pre-edit bytes the cache was holding.
func TestEditedSubjectReachesTheWire(t *testing.T) {
	s := openSeededStore(t)
	info := appendForEdit(t, s, "before")

	// A subject edit writes both tags, the way a client does: Export builds the header
	// from the normalized subject and falls back to PrSubject only when it is absent.
	if err := s.ModifyMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "after"},
		{Tag: mapi.PrNormalizedSubject, Value: "after"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "Subject: after") {
		t.Errorf("served message does not carry the edited subject:\n%s", got)
	}
	if strings.Contains(string(got), "Subject: before") {
		t.Error("served message still carries the pre-edit subject")
	}
}

// TestEditKeepsTheIndexSizeHonest holds the rebuild to rebuilding, not merely
// dropping, the cache. IMAP and POP3 report the size from the index without reading
// the message, so an invalidation that only deleted the file would leave the recorded
// size describing bytes no reader will ever be served.
func TestEditKeepsTheIndexSizeHonest(t *testing.T) {
	s := openSeededStore(t)
	info := appendForEdit(t, s, "short")

	if err := s.ModifyMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrNormalizedSubject, Value: strings.Repeat("a much longer subject ", 20)},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if size := indexSize(t, s, info.UID); size != int64(len(got)) {
		t.Errorf("index reports %d bytes, the message serves %d", size, len(got))
	}
}

// TestAddedAttachmentReachesTheWire proves an attachment built the way a ROP client
// builds one (an empty CreateAttachment, then the payload through
// SetAttachmentProperties) is visible to a reader. The payload arrives in the second
// call, so invalidating only on the first would still serve a message without it.
func TestAddedAttachmentReachesTheWire(t *testing.T) {
	s := openSeededStore(t)
	info := appendForEdit(t, s, "with attachment")

	aid, _, err := s.CreateAttachment(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAttachmentProperties(aid, mapi.PropertyValues{
		{Tag: mapi.PrAttachLongFilename, Value: "report.txt"},
		{Tag: mapi.PrAttachMimeTag, Value: "text/plain"},
		{Tag: mapi.PrAttachDataBin, Value: []byte("attached payload")},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "report.txt") {
		t.Errorf("the added attachment is not in the served message:\n%s", got)
	}
}

// TestDeletedAttachmentLeavesTheWire proves a removed attachment stops being served.
func TestDeletedAttachmentLeavesTheWire(t *testing.T) {
	s := openSeededStore(t)
	info := appendForEdit(t, s, "losing an attachment")

	_, num, err := s.CreateAttachment(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
		{Tag: mapi.PrAttachLongFilename, Value: "doomed.txt"},
		{Tag: mapi.PrAttachMimeTag, Value: "text/plain"},
		{Tag: mapi.PrAttachDataBin, Value: []byte("goes away")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(got), "doomed.txt") {
		t.Fatalf("the attachment was never served, so its removal proves nothing:\n%s", got)
	}

	if err := s.DeleteAttachment(info.ID, num); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "doomed.txt") {
		t.Errorf("the deleted attachment is still served:\n%s", got)
	}
}

// TestEditedRecipientReachesTheWire proves a recipient edit is served: the To header
// is serialized from the recipient rows, so a change there has to reach the wire form.
func TestEditedRecipientReachesTheWire(t *testing.T) {
	s := openSeededStore(t)
	info := appendForEdit(t, s, "recipient edit")

	recips, err := s.ListRecipients(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recips) == 0 {
		t.Fatal("the stored message has no recipients to edit")
	}
	if err := s.SetRecipientProperties(recips[0].ID, mapi.PropertyValues{
		{Tag: mapi.PrSmtpAddress, Value: "moved@example.test"},
		{Tag: mapi.PrEmailAddress, Value: "moved@example.test"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "moved@example.test") {
		t.Errorf("served message does not carry the edited recipient:\n%s", got)
	}
}

// TestEditKeepsAPreservedOriginalVerbatim proves the rebuild honours the preserved
// original: an S/MIME message edited in place is still served byte-for-byte. Passing
// it through Export instead would rebuild the MIME tree and destroy the signature.
func TestEditKeepsAPreservedOriginalVerbatim(t *testing.T) {
	s := openSeededStore(t)
	cert, key := genSignerCert(t)

	content := []byte("Content-Type: text/plain; charset=utf-8\r\n\r\nSigned and then edited.\r\n")
	body, err := smime.Sign(content, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	raw := append([]byte("From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: signed\r\n"), body...)
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}

	// An edit a client can perform on a signed message without touching its content:
	// flagging it for follow-up writes message properties.
	if err := s.ModifyMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrMsgStatus, Value: int32(1)},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Error("the edit rebuilt a preserved original instead of serving it verbatim")
	}
	if _, err := smime.Verify(got); err != nil {
		t.Errorf("the edited message no longer verifies: %v", err)
	}
}

// TestEditOfANonMailObjectWritesNoCache proves an edit never manufactures a wire-form
// cache for an object that has none. A contact or calendar item lives only in the
// object store and is never served over IMAP or POP3, so writing an eml for it would
// be pure waste on every edit.
func TestEditOfANonMailObjectWritesNoCache(t *testing.T) {
	s := openSeededStore(t)
	id, err := s.CreateMessage(mapi.PrivateFIDContacts, &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Contact"},
		{Tag: mapi.PrDisplayName, Value: "Ada Lovelace"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMessageProperties(id, mapi.PropertyValues{
		{Tag: mapi.PrDisplayName, Value: "Ada Byron"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.emlPath(midString(uint64(id)))); err == nil {
		t.Error("editing a contact created a wire-form cache for it")
	}
}
