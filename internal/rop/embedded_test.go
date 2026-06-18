package rop

import (
	"strings"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// buildOpenEmbeddedMessage builds a RopOpenEmbeddedMessage request: the input
// handle (inIdx) is the parent attachment, the output handle (outIdx) receives the
// embedded message, cpid is the session code page, and flags carries MAPI_MODIFY /
// MAPI_CREATE.
func buildOpenEmbeddedMessage(inIdx, outIdx, flags uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropOpenEmbeddedMessage)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint16(0x0FFF) // Cpid (session)
	b.Uint8(flags)
	return b.Bytes()
}

// emlWithEmbedded is a multipart/mixed message carrying a text body and a
// message/rfc822 attachment (an encapsulated message with its own subject + body).
const emlWithEmbedded = "From: fwd@hermex.test\r\nTo: dest@hermex.test\r\nSubject: Carrier\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"b0\"\r\n\r\n" +
	"--b0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n\r\nSee the attached message.\r\n" +
	"--b0\r\n" +
	"Content-Type: message/rfc822\r\n" +
	"Content-Disposition: attachment\r\n\r\n" +
	"From: orig@hermex.test\r\nTo: rcpt@hermex.test\r\nSubject: Inner Subject\r\n\r\nInner body line.\r\n" +
	"--b0--\r\n"

// emlWithFileAttach carries a plain (non-message) file attachment, used to prove
// OpenEmbeddedMessage refuses an attachment that is not an embedded message.
const emlWithFileAttach = "From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: WithFile\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"c0\"\r\n\r\n" +
	"--c0\r\n" +
	"Content-Type: text/plain\r\n\r\nBody.\r\n" +
	"--c0\r\n" +
	"Content-Type: application/octet-stream; name=\"f.bin\"\r\n" +
	"Content-Disposition: attachment; filename=\"f.bin\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n\r\nQUJD\r\n" +
	"--c0--\r\n"

// seedCarrier imports an .eml and stores it in the Inbox, returning the store
// message id. It is the proven import→store path the embedded tests build on.
func seedCarrier(t *testing.T, store *objectstore.Store, eml string) (*oxcmail.Message, int64) {
	t.Helper()
	msg, err := oxcmail.Import([]byte(eml), oxcmail.Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	mid, err := store.CreateMessage(int64(mapi.PrivateFIDInbox), msg)
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	return msg, mid
}

// TestOpenEmbeddedMessageRead drives the embedded-message read path: a stored
// message carrying a message/rfc822 attachment is opened, its attachment opened,
// and RopOpenEmbeddedMessage opens the encapsulated message. It proves the import
// flip tags the attachment as method-5 (afEmbeddedMessage) so a client knows to use
// OpenEmbeddedMessage, that the response carries the embedded message's normalized
// subject, and that GetProperties on the embedded handle reads the embedded body
// from the in-memory imported message.
func TestOpenEmbeddedMessageRead(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	store := sess.get(logonH).store

	carrier, mid := seedCarrier(t, store, emlWithEmbedded)
	if len(carrier.Attachments) != 1 {
		t.Fatalf("carrier has %d attachments, want 1 (the embedded message)", len(carrier.Attachments))
	}
	// The import flip reports the embedded message as method-5, the wire signal that
	// makes a client reach for OpenEmbeddedMessage instead of streaming the bytes.
	if m, _ := carrier.Attachments[0].Props.Get(mapi.PrAttachMethod); m != int32(mapi.AttachEmbeddedMsg) {
		t.Errorf("message/rfc822 attach method = %v, want %d (afEmbeddedMessage)", m, mapi.AttachEmbeddedMsg)
	}

	// OpenMessage -> OpenAttachment(0) -> OpenEmbeddedMessage.
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(mid)))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	_, h = sess.Dispatch(buildOpenAttachment(0, 1, 0), []uint32{msgH, 0xFFFFFFFF})
	attH := h[1]

	oem, h := sess.Dispatch(buildOpenEmbeddedMessage(0, 1, mapiModify), []uint32{attH, 0xFFFFFFFF})
	embH := h[1]
	p := ext.NewPull(oem, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropOpenEmbeddedMessage {
		t.Fatalf("OpenEmbeddedMessage RopId = %#x", id)
	}
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("OpenEmbeddedMessage ReturnValue = %#x", ec)
	}
	mustU8(t, p, "Reserved")
	if msgid, _ := p.Uint64(); msgid == 0 {
		t.Error("OpenEmbeddedMessage MessageId = 0, want a non-zero synthesized id")
	}
	mustU8(t, p, "HasNamedProperties")
	readTypedString(t, p) // SubjectPrefix
	if subj := readTypedString(t, p); subj != "Inner Subject" {
		t.Errorf("embedded normalized subject = %q, want Inner Subject", subj)
	}

	// GetProperties on the embedded handle reads the embedded message's body from the
	// in-memory imported message — the read ROP backing that makes the handle useful.
	cols := []mapi.PropTag{mapi.PrBody}
	gps, _ := sess.Dispatch(buildGetProps(ropGetPropertiesSpecific, 0, cols), []uint32{embH})
	p = ext.NewPull(gps, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("GetPropertiesSpecific(embedded) ReturnValue = %#x", ec)
	}
	row := decodeRow(t, p, cols)
	body, _ := row.Get(mapi.PrBody)
	if s, _ := body.(string); !strings.Contains(s, "Inner body line.") {
		t.Errorf("embedded body = %q, want it to contain %q", s, "Inner body line.")
	}
}

// TestOpenEmbeddedMessageErrors locks the refusal paths: OpenEmbeddedMessage on a
// non-attachment handle is unsupported; on a plain (non-embedded) attachment it
// reports not-found without MAPI_CREATE; and a MAPI_CREATE compose request is
// reported unsupported (the create path is deferred) rather than silently opening
// an empty message that could never be written back.
func TestOpenEmbeddedMessageErrors(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	store := sess.get(logonH).store

	_, mid := seedCarrier(t, store, emlWithFileAttach)
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(mid)))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]

	openEmbeddedEC := func(parentH uint32, flags uint8) uint32 {
		resp, _ := sess.Dispatch(buildOpenEmbeddedMessage(0, 1, flags), []uint32{parentH, 0xFFFFFFFF})
		p := ext.NewPull(resp, ext.FlagUTF16)
		mustU8(t, p, "RopId")
		mustU8(t, p, "ohindex")
		return mustU32(t, p, "ec")
	}

	// A message handle is not an attachment.
	if ec := openEmbeddedEC(msgH, mapiModify); ec != ecNotSupported {
		t.Errorf("OpenEmbeddedMessage on a message handle = %#x, want ecNotSupported", ec)
	}

	// A plain file attachment has no embedded message.
	_, h = sess.Dispatch(buildOpenAttachment(0, 1, 0), []uint32{msgH, 0xFFFFFFFF})
	attH := h[1]
	if ec := openEmbeddedEC(attH, 0); ec != ecNotFound {
		t.Errorf("OpenEmbeddedMessage on a plain attachment = %#x, want ecNotFound", ec)
	}
	// MAPI_CREATE compose is deferred (reported, not silently stubbed).
	if ec := openEmbeddedEC(attH, mapiCreate); ec != ecNotSupported {
		t.Errorf("OpenEmbeddedMessage MAPI_CREATE = %#x, want ecNotSupported (compose deferred)", ec)
	}
}
