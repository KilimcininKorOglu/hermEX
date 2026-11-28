package rop

import (
	"bytes"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// TestSubmitCarriesTheAttachments submits a message composed with an attachment and
// checks the recipient receives the attachment; also verifies a submitted message
// with no recipients is refused and leaves the recipient inbox untouched.
func TestSubmitCarriesTheAttachments(t *testing.T) {
	ownerDir, aliceDir := provisionedDir(t), provisionedDir(t)
	accounts := directory.StaticAccounts{
		"owner@hermex.test": {MailboxPath: ownerDir},
		"alice@hermex.test": {MailboxPath: aliceDir},
	}
	sess := NewSession(ownerDir, accounts, "owner@hermex.test")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	msgH := dispatchOK(t, sess, buildCreateMessage(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDraft))), ropCreateMessage, "CreateMessage", logonH, 0xFFFFFFFF)[1]
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Note"},
		{Tag: mapi.PrSubject, Value: "Report"},
		{Tag: mapi.PrBody, Value: "See the attached file."},
	}), ropSetProperties, "SetProperties(msg)", msgH)

	// Hostile: submitting an unsaved draft or a message without recipients is refused
	// and leaves the recipient inbox untouched.
	unsavedSub := dispatchOn(sess, buildSubmitMessage(0), msgH)
	wantEC(t, unsavedSub, ropSubmitMessage, ecNotSupported, "SubmitMessage before SaveChangesMessage")

	dispatchOK(t, sess, buildSaveChangesMessage(0, 1), ropSaveChangesMessage, "SaveChangesMessage(empty recipients)", logonH, msgH)
	badSub := dispatchOn(sess, buildSubmitMessage(0), msgH)
	wantEC(t, badSub, ropSubmitMessage, ecNotFound, "SubmitMessage without recipients")
	if n := inboxCount(t, aliceDir); n != 0 {
		t.Fatalf("alice inbox after refused submit = %d, want 0", n)
	}

	toRow := buildSMTPRecipientRow(0, mapi.RecipTo, "alice@hermex.test", "Alice")
	dispatchOK(t, sess, buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, toRow), ropModifyRecipients, "ModifyRecipients", msgH)

	num, attH := createAttachmentNum(t, sess, msgH)
	if num != 0 {
		t.Fatalf("first attach number = %d, want 0", num)
	}
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
		{Tag: mapi.PrAttachLongFilename, Value: "figures.txt"},
		{Tag: mapi.PrAttachDataBin, Value: []byte("QUARTERLY-FIGURES")},
	}), ropSetProperties, "SetProperties(attach)", attH)
	dispatchOK(t, sess, buildSaveChangesAttachment(0, 1), ropSaveChangesAttachment, "SaveChangesAttachment", msgH, attH)
	dispatchOK(t, sess, buildSaveChangesMessage(0, 1), ropSaveChangesMessage, "SaveChangesMessage", logonH, msgH)
	dispatchOK(t, sess, buildSubmitMessage(0), ropSubmitMessage, "SubmitMessage", msgH)

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
	ownerDir, aliceDir := provisionedDir(t), provisionedDir(t)
	accounts := directory.StaticAccounts{
		"owner@hermex.test": {MailboxPath: ownerDir},
		"alice@hermex.test": {MailboxPath: aliceDir},
	}
	sess := NewSession(ownerDir, accounts, "owner@hermex.test")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	msgH := dispatchOK(t, sess, buildCreateMessage(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDraft))), ropCreateMessage, "CreateMessage", logonH, 0xFFFFFFFF)[1]
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Note.SMIME.MultipartSigned"},
		{Tag: mapi.PrSubject, Value: "Signed"},
	}), ropSetProperties, "SetProperties(msg)", msgH)
	toRow := buildSMTPRecipientRow(0, mapi.RecipTo, "alice@hermex.test", "Alice")
	dispatchOK(t, sess, buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, toRow), ropModifyRecipients, "ModifyRecipients", msgH)
	num, attH := createAttachmentNum(t, sess, msgH)
	if num != 0 {
		t.Fatalf("signed attach number = %d, want 0", num)
	}
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
		{Tag: mapi.PrAttachMimeTag, Value: "multipart/signed"},
		{Tag: mapi.PrAttachLongFilename, Value: "SMIME.txt"},
		{Tag: mapi.PrAttachDataBin, Value: []byte(entity)},
	}), ropSetProperties, "SetProperties(signed attach)", attH)
	dispatchOK(t, sess, buildSaveChangesAttachment(0, 1), ropSaveChangesAttachment, "SaveChangesAttachment", msgH, attH)
	dispatchOK(t, sess, buildSaveChangesMessage(0, 1), ropSaveChangesMessage, "SaveChangesMessage", logonH, msgH)
	dispatchOK(t, sess, buildSubmitMessage(0), ropSubmitMessage, "SubmitMessage", msgH)

	raw := firstInboxRaw(t, aliceDir)
	if !bytes.Contains(raw, []byte(entity)) || bytes.Contains(raw, []byte("multipart/mixed")) {
		t.Errorf("the signed entity did not arrive as the message:\n%s", raw)
	}
}
