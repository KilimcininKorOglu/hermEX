package rop

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/relay"
)

// TestSubmitSendsTNEFToARichInfoRecipient submits one message to a recipient whose
// PidTagSendRichInfo is true and to one whose value is false, both outside this
// server, and reads what the relay queued for each. The first must get the TNEF
// form, the second plain MIME: without the split every recipient got plain MIME,
// and a rich-info recipient lost what MIME cannot carry.
func TestSubmitSendsTNEFToARichInfoRecipient(t *testing.T) {
	ownerDir := t.TempDir()
	accounts := directory.StaticAccounts{"owner@hermex.test": {MailboxPath: ownerDir}}
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	sess := NewSession(ownerDir, accounts, "owner@hermex.test", WithSpool(sp))
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildCreateMessage(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDraft))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Note"},
		{Tag: mapi.PrSubject, Value: "Rich"},
		{Tag: mapi.PrBody, Value: "body"},
	}), []uint32{msgH})
	plain := buildSMTPRecipientRow(0, mapi.RecipTo, "carol@external.test", "Carol")
	rich := buildSMTPRecipientRow(1, mapi.RecipTo, "dave@external.test", "Dave")
	// The flag word follows the row id (4 bytes), type (1) and size (2), little-endian.
	rich[8] |= byte(recipientRowNonRich >> 8)
	mr, _ := sess.Dispatch(buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, plain, rich), []uint32{msgH})
	ropOK(t, mr, ropModifyRecipients, "ModifyRecipients")
	sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})
	sub, _ := sess.Dispatch(buildSubmitMessage(0), []uint32{msgH})
	ropOK(t, sub, ropSubmitMessage, "SubmitMessage")

	due, err := sp.Claim(time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string][]byte{}
	for _, it := range due {
		body[it.Recipient] = it.Body
	}
	tnefType := []byte("application/ms-tnef")
	if raw := body["dave@external.test"]; !bytes.Contains(raw, tnefType) || !bytes.Contains(raw, []byte("X-MS-TNEF-Correlator: <")) {
		t.Errorf("the rich-info recipient got no TNEF form:\n%s", raw)
	}
	if raw := body["carol@external.test"]; raw == nil || bytes.Contains(raw, tnefType) {
		t.Errorf("the plain recipient did not get plain MIME:\n%s", raw)
	}
}
