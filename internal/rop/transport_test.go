package rop

import (
	"testing"

	"hermex/internal/directory"
	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// buildTransportHeaderOnly builds a header-only transport ROP request (RopId,
// LogonId, InputHandleIndex) — the wire shape of RopSetSpooler and
// RopGetTransportFolder, neither of which carries fields beyond the head.
func buildTransportHeaderOnly(ropID, inIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropID)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	return b.Bytes()
}

// transportSession opens a session and returns its logon handle, the handle the
// transport ROPs resolve.
func transportSession(t *testing.T) (*Session, uint32) {
	t.Helper()
	sess := NewSession(t.TempDir(), nil, "")
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	return sess, h[0]
}

// TestSetSpooler confirms RopSetSpooler answers a private-mailbox logon with the
// bare 6-byte head and ecSuccess, consuming no trailing bytes.
func TestSetSpooler(t *testing.T) {
	sess, logonH := transportSession(t)
	defer sess.Close()

	resp, _ := sess.Dispatch(buildTransportHeaderOnly(ropSetSpooler, 0), []uint32{logonH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSetSpooler {
		t.Fatalf("RopId = %#x, want SetSpooler", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("SetSpooler ReturnValue = %#x", ec)
	}
	if p.Remaining() != 0 {
		t.Fatalf("SetSpooler response has %d trailing bytes; the contract is a bare head", p.Remaining())
	}
}

// TestGetTransportFolder confirms RopGetTransportFolder returns the Outbox folder
// id as an EID after the head — the id a client deposits outgoing mail into.
func TestGetTransportFolder(t *testing.T) {
	sess, logonH := transportSession(t)
	defer sess.Close()

	resp, _ := sess.Dispatch(buildTransportHeaderOnly(ropGetTransportFolder, 0), []uint32{logonH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropGetTransportFolder {
		t.Fatalf("RopId = %#x, want GetTransportFolder", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("GetTransportFolder ReturnValue = %#x", ec)
	}
	fid, err := p.Uint64()
	if err != nil {
		t.Fatalf("FolderId: %v", err)
	}
	if gcv := mapi.EID(fid).GCValue(); gcv != mapi.PrivateFIDOutbox {
		t.Errorf("FolderId GCV = %#x, want PrivateFIDOutbox (%#x)", gcv, mapi.PrivateFIDOutbox)
	}
	if p.Remaining() != 0 {
		t.Errorf("GetTransportFolder response has %d trailing bytes after FolderId", p.Remaining())
	}
}

// folderMsgCount returns the number of messages in a mailbox folder.
func folderMsgCount(t *testing.T, dir string, folderID int64) int {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(folderID)
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

// TestTransportSendDelivers composes a message and sends it through RopTransportSend,
// proving it reaches the recipient with the NoPropertiesReturned response and — the
// behaviour that distinguishes it from RopSubmitMessage — that it files NO Sent
// Items copy (the reference does not file one here, leaving disposition to the client).
func TestTransportSendDelivers(t *testing.T) {
	ownerDir, aliceDir := t.TempDir(), t.TempDir()
	accounts := directory.StaticAccounts{
		"owner@hermex.test": {MailboxPath: ownerDir},
		"alice@hermex.test": {MailboxPath: aliceDir},
	}
	draftsEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDraft))

	sess := NewSession(ownerDir, accounts, "owner@hermex.test")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	_, h = sess.Dispatch(buildCreateMessage(0, 1, draftsEID), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "XPORTSEND"},
		{Tag: mapi.PrBody, Value: "sent via transport"},
	}), []uint32{msgH})
	toRow := buildSMTPRecipientRow(0, mapi.RecipTo, "alice@hermex.test", "Alice")
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
	if npr := mustU8(t, p, "NoPropertiesReturned"); npr != 1 {
		t.Errorf("NoPropertiesReturned = %d, want 1 (v1 returns no property list)", npr)
	}
	if p.Remaining() != 0 {
		t.Errorf("TransportSend response has %d trailing bytes after the flag", p.Remaining())
	}

	if n := inboxCount(t, aliceDir); n != 1 {
		t.Errorf("alice inbox = %d, want 1 (transport-sent message)", n)
	}
	// The distinguishing behaviour: RopSubmitMessage files a Sent Items copy here,
	// RopTransportSend does not.
	if n := folderMsgCount(t, ownerDir, int64(mapi.PrivateFIDSentItems)); n != 0 {
		t.Errorf("owner Sent Items = %d, want 0 (TransportSend files no Sent copy)", n)
	}
}
