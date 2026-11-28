package rop

import (
	"strings"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// buildOpenEmbeddedMessage builds a RopOpenEmbeddedMessage request.
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

// seedCarrier imports an .eml and stores it in the Inbox, returning the store message id.
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

// TestOpenEmbeddedMessageRead drives the embedded-message read path.
func TestOpenEmbeddedMessageRead(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store

	carrier, mid := seedCarrier(t, store, emlWithEmbedded)
	if len(carrier.Attachments) != 1 {
		t.Fatalf("carrier has %d attachments, want 1 (the embedded message)", len(carrier.Attachments))
	}
	if m := rowProp(t, carrier.Attachments[0].Props, mapi.PrAttachMethod, "attach method"); m != int32(mapi.AttachEmbeddedMsg) {
		t.Errorf("message/rfc822 attach method = %v, want %d (afEmbeddedMessage)", m, mapi.AttachEmbeddedMsg)
	}

	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(mid)))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	attH := dispatchOK(t, sess, buildOpenAttachment(0, 1, 0), ropOpenAttachment, "OpenAttachment", msgH, 0xFFFFFFFF)[1]

	oem, h := sess.Dispatch(buildOpenEmbeddedMessage(0, 1, mapiModify), []uint32{attH, 0xFFFFFFFF})
	embH := h[1]
	p := ropOK(t, oem, ropOpenEmbeddedMessage, "OpenEmbeddedMessage")
	mustU8(t, p, "Reserved")
	if msgid := mustU64(t, p, "MessageId"); msgid == 0 {
		t.Error("OpenEmbeddedMessage MessageId = 0, want a non-zero synthesized id")
	}
	mustU8(t, p, "HasNamedProperties")
	readTypedString(t, p) // SubjectPrefix
	if subj := readTypedString(t, p); subj != "Inner Subject" {
		t.Errorf("embedded normalized subject = %q, want Inner Subject", subj)
	}

	cols := []mapi.PropTag{mapi.PrBody}
	gps := dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), embH)
	row := decodeRow(t, ropOK(t, gps, ropGetPropertiesSpecific, "GetPropertiesSpecific(embedded)"), cols)
	body := rowProp(t, row, mapi.PrBody, "embedded body")
	s, ok := body.(string)
	if !ok {
		t.Fatalf("embedded body = %T, want string", body)
	}
	if !strings.Contains(s, "Inner body line.") {
		t.Errorf("embedded body = %q, want it to contain %q", s, "Inner body line.")
	}
}

// TestOpenEmbeddedMessageErrors locks the refusal paths and verifies output handle slots remain unset.
func TestOpenEmbeddedMessageErrors(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store

	_, mid := seedCarrier(t, store, emlWithFileAttach)
	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(mid)))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]

	expectRefusal := func(parentH uint32, flags uint8, want uint32, what string) {
		t.Helper()
		resp, handles := sess.Dispatch(buildOpenEmbeddedMessage(0, 1, flags), []uint32{parentH, 0xFFFFFFFF})
		wantEC(t, resp, ropOpenEmbeddedMessage, want, what)
		if len(handles) != 2 || handles[1] != 0xFFFFFFFF {
			t.Errorf("%s left handles = %#x, want slot 1 unset", what, handles)
		}
	}

	expectRefusal(0xFFFFFFFF, 0, ecNotSupported, "OpenEmbeddedMessage(null handle)")
	expectRefusal(msgH, mapiModify, ecNotSupported, "OpenEmbeddedMessage(message handle)")

	attH := dispatchOK(t, sess, buildOpenAttachment(0, 1, 0), ropOpenAttachment, "OpenAttachment", msgH, 0xFFFFFFFF)[1]
	expectRefusal(attH, 0, ecNotFound, "OpenEmbeddedMessage(plain attachment)")
	expectRefusal(attH, mapiCreate, ecNotSupported, "OpenEmbeddedMessage(MAPI_CREATE on opened)")
}

// TestComposeEmbeddedMessage drives the embedded-message write path end to end.
func TestComposeEmbeddedMessage(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "CARRIER"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store

	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	num, attH := createAttachmentNum(t, sess, msgH)
	if num != 0 {
		t.Fatalf("attach number = %d, want 0", num)
	}

	embH := dispatchOK(t, sess, buildOpenEmbeddedMessage(0, 1, mapiCreate), ropOpenEmbeddedMessage, "OpenEmbeddedMessage(MAPI_CREATE)", attH, 0xFFFFFFFF)[1]

	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "Composed Inner"},
		{Tag: mapi.PrBody, Value: "Composed body."},
	}), ropSetProperties, "SetProperties(embedded)", embH)
	dispatchOK(t, sess, buildSaveChangesMessage(0, 1), ropSaveChangesMessage, "SaveChangesMessage(embedded)", logonH, embH)
	dispatchOK(t, sess, buildSaveChangesAttachment(0, 1), ropSaveChangesAttachment, "SaveChangesAttachment", msgH, attH)
	saveChangesEID(t, dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, msgH))

	assertAttachmentCount(t, store, mid, 1, "carrier")
	saved, err := store.OpenMessage(int64(mid))
	if err != nil {
		t.Fatal(err)
	}
	wantProp(t, saved.Attachments[0].Props, mapi.PrAttachMethod, int32(mapi.AttachEmbeddedMsg),
		"composed attachment method (afEmbeddedMessage)")

	reMsgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), ropOpenMessage, "OpenMessage(read-back)", logonH, 0xFFFFFFFF)[1]
	reAttH := dispatchOK(t, sess, buildOpenAttachment(0, 1, 0), ropOpenAttachment, "OpenAttachment(read-back)", reMsgH, 0xFFFFFFFF)[1]
	oem2, h := sess.Dispatch(buildOpenEmbeddedMessage(0, 1, mapiModify), []uint32{reAttH, 0xFFFFFFFF})
	reEmbH := h[1]
	p := ropOK(t, oem2, ropOpenEmbeddedMessage, "OpenEmbeddedMessage(read-back)")
	mustU8(t, p, "Reserved")
	mustU64(t, p, "MessageId")
	mustU8(t, p, "HasNamedProperties")
	readTypedString(t, p) // SubjectPrefix
	if subj := readTypedString(t, p); subj != "Composed Inner" {
		t.Errorf("read-back embedded subject = %q, want Composed Inner", subj)
	}

	cols := []mapi.PropTag{mapi.PrBody}
	gps := dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), reEmbH)
	p = ropOK(t, gps, ropGetPropertiesSpecific, "GetPropertiesSpecific(read-back embedded)")
	body := rowProp(t, decodeRow(t, p, cols), mapi.PrBody, "read-back embedded body")
	s, ok := body.(string)
	if !ok {
		t.Fatalf("read-back embedded body = %T, want string", body)
	}
	if !strings.Contains(s, "Composed body.") {
		t.Errorf("read-back embedded body = %q, want to contain %q", s, "Composed body.")
	}
}

const emlNestedAttach = "From: fwd@hermex.test\r\nTo: dest@hermex.test\r\nSubject: Carrier2\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"out\"\r\n\r\n" +
	"--out\r\n" +
	"Content-Type: text/plain\r\n\r\nOuter body.\r\n" +
	"--out\r\n" +
	"Content-Type: message/rfc822\r\n" +
	"Content-Disposition: attachment\r\n\r\n" +
	"From: orig@hermex.test\r\nTo: rcpt@hermex.test\r\nSubject: Nested Subject\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"in\"\r\n\r\n" +
	"--in\r\n" +
	"Content-Type: text/plain\r\n\r\nNested body line.\r\n" +
	"--in\r\n" +
	"Content-Type: application/octet-stream; name=\"inner.bin\"\r\n" +
	"Content-Disposition: attachment; filename=\"inner.bin\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n\r\nSGVsbG8=\r\n" +
	"--in--\r\n" +
	"--out--\r\n"

// openReadStreamAll opens a read-only stream over a property of parentH and returns its full bytes.
func openReadStreamAll(t *testing.T, sess *Session, parentH uint32, tag uint32) []byte {
	t.Helper()
	os, h := sess.Dispatch(buildOpenStream(0, 1, tag, 0), []uint32{parentH, 0xFFFFFFFF})
	p := ropOK(t, os, ropOpenStream, "OpenStream")
	mustU32(t, p, "StreamSize")
	return readStreamChunk(t, sess, h[1], 0xBABE, 0xFFFF)
}

// TestEmbeddedMessageReadSurface proves the read ROPs are kind-aware for an embedded message.
func TestEmbeddedMessageReadSurface(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store

	_, mid := seedCarrier(t, store, emlNestedAttach)

	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(mid)))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	attH := dispatchOK(t, sess, buildOpenAttachment(0, 1, 0), ropOpenAttachment, "OpenAttachment", msgH, 0xFFFFFFFF)[1]
	embH := dispatchOK(t, sess, buildOpenEmbeddedMessage(0, 1, mapiModify), ropOpenEmbeddedMessage, "OpenEmbeddedMessage", attH, 0xFFFFFFFF)[1]

	rc := dispatchOn(sess, buildReloadCachedInfo(0), embH)
	p := ropOK(t, rc, ropReloadCachedInfo, "ReloadCachedInformation(embedded)")
	mustU8(t, p, "HasNamedProperties")
	readTypedString(t, p) // SubjectPrefix
	if subj := readTypedString(t, p); subj != "Nested Subject" {
		t.Errorf("ReloadCachedInformation embedded subject = %q, want Nested Subject", subj)
	}

	if got := decodeUTF16LE(openReadStreamAll(t, sess, embH, uint32(mapi.PrBody))); !strings.Contains(got, "Nested body line.") {
		t.Errorf("embedded body stream = %q, want it to contain %q", got, "Nested body line.")
	}

	tableH := dispatchOK(t, sess, buildGetAttachmentTable(0, 1), ropGetAttachmentTable, "GetAttachmentTable(embedded)", embH, 0xFFFFFFFF)[1]
	cols := []mapi.PropTag{mapi.PrAttachNum, mapi.PrAttachLongFilename}
	dispatchOK(t, sess, buildSetColumns(0, cols), ropSetColumns, "SetColumns(embedded attach)", tableH)
	qr := dispatchOn(sess, buildQueryRows(0, 0, 1, 32), tableH)
	_, rows := queryRowsResponse(t, qr, cols)
	if len(rows) != 1 {
		t.Fatalf("embedded attachment table rows = %d, want 1 (the nested attachment)", len(rows))
	}
	if fn := rowProp(t, rows[0], mapi.PrAttachLongFilename, "nested attachment filename"); fn != "inner.bin" {
		t.Errorf("nested attachment filename = %v, want inner.bin", fn)
	}

	nAttH := dispatchOK(t, sess, buildOpenAttachment(0, 1, 0), ropOpenAttachment, "OpenAttachment(nested)", embH, 0xFFFFFFFF)[1]
	if data := openReadStreamAll(t, sess, nAttH, uint32(mapi.PrAttachDataBin)); string(data) != "Hello" {
		t.Errorf("nested attachment data = %q, want Hello", data)
	}
}
