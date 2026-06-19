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
	ownerDir := t.TempDir()
	accounts := directory.StaticAccounts{"owner@hermex.test": {MailboxPath: ownerDir}}
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	draftsEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDraft))

	sess := NewSession(ownerDir, accounts, "owner@hermex.test", WithSpool(sp))
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	_, h = sess.Dispatch(buildCreateMessage(0, 1, draftsEID), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "RELAYOUT"},
		{Tag: mapi.PrBody, Value: "to carol"},
	}), []uint32{msgH})
	toRow := buildSMTPRecipientRow(0, mapi.RecipTo, "carol@external.test", "Carol")
	sess.Dispatch(buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, toRow), []uint32{msgH})
	sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})

	resp, _ := sess.Dispatch(buildTransportHeaderOnly(ropTransportSend, 0), []uint32{msgH})
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
