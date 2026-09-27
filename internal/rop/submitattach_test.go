package rop

import (
	"bytes"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// TestSubmitCarriesTheAttachments submits a message composed with an attachment and
// checks the recipient receives the attachment. The submit exported the message
// from its properties and recipients alone, so every attachment a MAPI client
// added was dropped from the mail that went out.
func TestSubmitCarriesTheAttachments(t *testing.T) {
	ownerDir, aliceDir := t.TempDir(), t.TempDir()
	accounts := directory.StaticAccounts{
		"owner@hermex.test": {MailboxPath: ownerDir},
		"alice@hermex.test": {MailboxPath: aliceDir},
	}
	sess := NewSession(ownerDir, accounts, "owner@hermex.test")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildCreateMessage(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDraft))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Note"},
		{Tag: mapi.PrSubject, Value: "Report"},
		{Tag: mapi.PrBody, Value: "See the attached file."},
	}), []uint32{msgH})
	toRow := buildSMTPRecipientRow(0, mapi.RecipTo, "alice@hermex.test", "Alice")
	sess.Dispatch(buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, toRow), []uint32{msgH})

	_, attH := createAttachmentNum(t, sess, msgH)
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
		{Tag: mapi.PrAttachLongFilename, Value: "figures.txt"},
		{Tag: mapi.PrAttachDataBin, Value: []byte("QUARTERLY-FIGURES")},
	}), []uint32{attH})
	sa, _ := sess.Dispatch(buildSaveChangesAttachment(0, 1), []uint32{msgH, attH})
	ropOK(t, sa, ropSaveChangesAttachment, "SaveChangesAttachment")
	sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})
	sub, _ := sess.Dispatch(buildSubmitMessage(0), []uint32{msgH})
	ropOK(t, sub, ropSubmitMessage, "SubmitMessage")

	raw := firstInboxRaw(t, aliceDir)
	if !bytes.Contains(raw, []byte("figures.txt")) {
		t.Errorf("delivered message lost its attachment:\n%s", raw)
	}
}

// TestMailAttachmentsDropsExceptions keeps a meeting's exception attachments out of
// the mail: the iCalendar part carries each as an override.
func TestMailAttachmentsDropsExceptions(t *testing.T) {
	file := oxcmail.Attachment{Props: mapi.PropertyValues{{Tag: mapi.PrAttachLongFilename, Value: "agenda.pdf"}}}
	exception := oxcmail.Attachment{Props: mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachEmbeddedMsg)},
		{Tag: mapi.PrAttachmentFlags, Value: int32(mapi.AttachmentFlagException)},
	}}
	got := mailAttachments([]oxcmail.Attachment{exception, file})
	if len(got) != 1 || got[0].Props[0].Value != "agenda.pdf" {
		t.Errorf("mail attachments = %+v, want the file alone", got)
	}
}

// TestSubmitSendsAClearSignedMessage submits the shape Outlook gives a signed
// message ([MS-OXOSMIME]): its multipart/signed entity in one attachment. The
// recipient must receive that entity as the message, not wrapped as a file.
func TestSubmitSendsAClearSignedMessage(t *testing.T) {
	const entity = "Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; micalg=sha-256; boundary=\"b1\"\r\n" +
		"\r\n--b1\r\nContent-Type: text/plain\r\n\r\nsigned text\r\n--b1\r\nContent-Type: application/pkcs7-signature\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nMIIB\r\n--b1--\r\n"
	ownerDir, aliceDir := t.TempDir(), t.TempDir()
	accounts := directory.StaticAccounts{
		"owner@hermex.test": {MailboxPath: ownerDir},
		"alice@hermex.test": {MailboxPath: aliceDir},
	}
	sess := NewSession(ownerDir, accounts, "owner@hermex.test")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildCreateMessage(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDraft))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Note.SMIME.MultipartSigned"},
		{Tag: mapi.PrSubject, Value: "Signed"},
	}), []uint32{msgH})
	toRow := buildSMTPRecipientRow(0, mapi.RecipTo, "alice@hermex.test", "Alice")
	sess.Dispatch(buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, toRow), []uint32{msgH})
	_, attH := createAttachmentNum(t, sess, msgH)
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
		{Tag: mapi.PrAttachMimeTag, Value: "multipart/signed"},
		{Tag: mapi.PrAttachLongFilename, Value: "SMIME.txt"},
		{Tag: mapi.PrAttachDataBin, Value: []byte(entity)},
	}), []uint32{attH})
	sess.Dispatch(buildSaveChangesAttachment(0, 1), []uint32{msgH, attH})
	sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})
	sub, _ := sess.Dispatch(buildSubmitMessage(0), []uint32{msgH})
	ropOK(t, sub, ropSubmitMessage, "SubmitMessage")

	raw := firstInboxRaw(t, aliceDir)
	if !bytes.Contains(raw, []byte(entity)) || bytes.Contains(raw, []byte("multipart/mixed")) {
		t.Errorf("the signed entity did not arrive as the message:\n%s", raw)
	}
}
