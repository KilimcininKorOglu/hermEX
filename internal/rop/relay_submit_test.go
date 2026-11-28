package rop

import (
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/relay"
)

// TestTransportSendRelaysExternal proves a ROP submission to a foreign-domain
// recipient is queued for outbound relay rather than dropped: with a spool on the
// session, the external recipient leaves through the relay carrying the owner's
// address as the envelope From.
func TestTransportSendRelaysExternal(t *testing.T) {
	ownerDir := provisionedDir(t)
	accounts := directory.StaticAccounts{"owner@hermex.test": {MailboxPath: ownerDir}}
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, sp)
	draftsEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDraft))

	sess := NewSession(ownerDir, accounts, "owner@hermex.test", WithSpool(sp))
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]

	h = dispatchOK(t, sess, buildCreateMessage(0, 1, draftsEID), ropCreateMessage, "CreateMessage", logonH, 0xFFFFFFFF)
	msgH := h[1]
	ropOK(t, dispatchOn(sess, buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "RELAYOUT"},
		{Tag: mapi.PrBody, Value: "to carol"},
	}), msgH), ropSetProperties, "SetProperties")
	toRow := buildSMTPRecipientRow(0, mapi.RecipTo, "carol@external.test", "Carol")
	ropOK(t, dispatchOn(sess, buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, toRow), msgH), ropModifyRecipients, "ModifyRecipients")
	ropOK(t, dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, msgH), ropSaveChangesMessage, "SaveChangesMessage")

	resp := dispatchOn(sess, buildTransportHeaderOnly(ropTransportSend, 0), msgH)
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropTransportSend {
		t.Fatalf("RopId = %#x, want TransportSend", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("TransportSend ReturnValue = %#x", ec)
	}

	due, err := sp.Claim(time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Recipient != "carol@external.test" {
		t.Fatalf("relay spool = %v, want carol@external.test queued for relay", due)
	}
	if due[0].From != "owner@hermex.test" {
		t.Errorf("relay envelope From = %q, want owner@hermex.test", due[0].From)
	}
}

// TestTransportSendNoRecipientRefused proves submitting a saved draft without
// any routable recipient fails with ecNotFound and leaves the relay spool and
// Sent Items empty.
func TestTransportSendNoRecipientRefused(t *testing.T) {
	ownerDir := provisionedDir(t)
	accounts := directory.StaticAccounts{"owner@hermex.test": {MailboxPath: ownerDir}}
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	mustNil(t, err)
	defer mustClose(t, sp)
	draftsEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDraft))

	sess := NewSession(ownerDir, accounts, "owner@hermex.test", WithSpool(sp))
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]
	h = dispatchOK(t, sess, buildCreateMessage(0, 1, draftsEID), ropCreateMessage, "CreateMessage", logonH, 0xFFFFFFFF)
	msgH := h[1]
	ropOK(t, dispatchOn(sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "EMPTY"}}), msgH), ropSetProperties, "SetProperties")
	ropOK(t, dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, msgH), ropSaveChangesMessage, "SaveChangesMessage")

	resp := dispatchOn(sess, buildTransportHeaderOnly(ropTransportSend, 0), msgH)
	if ec := readEC(t, resp, ropTransportSend); ec != ecNotFound {
		t.Errorf("TransportSend(no recipient) ec = %#x, want ecNotFound (%#x)", ec, ecNotFound)
	}
	due, err := sp.Claim(time.Now(), 10)
	mustNil(t, err)
	if len(due) != 0 {
		t.Errorf("relay spool = %v after refused TransportSend, want empty", due)
	}
	if n := sentItemsCount(t, ownerDir); n != 0 {
		t.Errorf("owner Sent Items = %d after refused TransportSend, want 0", n)
	}
}
