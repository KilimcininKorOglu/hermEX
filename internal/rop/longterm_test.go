package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

func buildLongTermIdFromId(objID uint64) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropLongTermIdFromId)
	b.Uint8(0) // logon_id
	b.Uint8(0) // hindex -> logon handle
	b.Uint64(objID)
	return b.Bytes()
}

func buildIdFromLongTermId(guid mapi.GUID, gc mapi.GlobCnt) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropIdFromLongTermId)
	b.Uint8(0)
	b.Uint8(0)
	b.GUID(guid)
	b.Raw(gc[:])
	b.Uint16(0) // padding
	return b.Bytes()
}

// TestLongTermIdRoundTrip confirms a short-term object id converts to a long-term id
// and back to the identical id, and that unknown replica IDs, foreign GUIDs, and
// non-logon handles are refused while keeping valid round-trips working.
func TestLongTermIdRoundTrip(t *testing.T) {
	dir := provisionedDir(t)
	seedInboxMessage(t, dir, "LTID") // seeds the mailbox (store GUID) and a message

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]

	objEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	lt := dispatchOn(sess, buildLongTermIdFromId(objEID), logonH)
	p := ropOK(t, lt, ropLongTermIdFromId, "LongTermIdFromId")
	guid, err := p.GUID()
	if err != nil {
		t.Fatal(err)
	}
	gcBytes, err := p.Raw(6)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Uint16(); err != nil { // padding
		t.Fatal(err)
	}
	var gc mapi.GlobCnt
	copy(gc[:], gcBytes)

	idResp := dispatchOn(sess, buildIdFromLongTermId(guid, gc), logonH)
	p2 := ropOK(t, idResp, ropIdFromLongTermId, "IdFromLongTermId")
	got, err := p2.Uint64()
	if err != nil {
		t.Fatal(err)
	}
	if got != objEID {
		t.Errorf("round-trip id = %#x, want %#x", got, objEID)
	}

	// Hostile: foreign replica GUID, unknown short-term ReplID, and null/non-logon handles.
	foreign := mapi.GUID{Data1: 0xBADC0DE}
	wantEC(t, dispatchOn(sess, buildIdFromLongTermId(foreign, gc), logonH), ropIdFromLongTermId, ecInvalidParam, "IdFromLongTermId(foreign GUID)")
	wantEC(t, dispatchOn(sess, buildLongTermIdFromId(uint64(mapi.MakeEIDEx(99, mapi.PrivateFIDInbox))), logonH), ropLongTermIdFromId, ecNotFound, "LongTermIdFromId(unknown replid)")
	wantEC(t, dispatchOn(sess, buildLongTermIdFromId(objEID), 0xFFFFFFFF), ropLongTermIdFromId, ecNotSupported, "LongTermIdFromId(null handle)")
	wantEC(t, dispatchOn(sess, buildIdFromLongTermId(guid, gc), 0xFFFFFFFF), ropIdFromLongTermId, ecNotSupported, "IdFromLongTermId(null handle)")

	// Valid round-trip continues to work after refusals.
	retryResp := dispatchOn(sess, buildIdFromLongTermId(guid, gc), logonH)
	pRetry := ropOK(t, retryResp, ropIdFromLongTermId, "IdFromLongTermId after refusals")
	gotRetry, err := pRetry.Uint64()
	if err != nil {
		t.Fatal(err)
	}
	if gotRetry != objEID {
		t.Errorf("retry id = %#x, want %#x", gotRetry, objEID)
	}
}

// TestLogonReplicaGUIDResolves proves the replica GUID the logon pairs with its
// ReplId in LOGON_PMB_RESPONSE ([MS-OXCSTOR] 2.2.1.1.3), the mapping signature,
// resolves both ways: a long-term id carrying it maps to that ReplId, and an id
// carrying that ReplId maps back to it.
func TestLogonReplicaGUIDResolves(t *testing.T) {
	dir := provisionedDir(t)
	seedInboxMessage(t, dir, "LTID")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	sig, err := sess.get(logonH).store.MappingSignature()
	if err != nil {
		t.Fatal(err)
	}
	gc := mapi.ValueToGC(0x1234)

	idResp := dispatchOn(sess, buildIdFromLongTermId(sig, gc), logonH)
	p := ropOK(t, idResp, ropIdFromLongTermId, "IdFromLongTermId(mapping signature)")
	id, err := p.Uint64()
	if err != nil {
		t.Fatal(err)
	}
	if replid := mapi.EID(id).ReplID(); replid != privateReplID {
		t.Errorf("replid = %d, want %d, the one the logon pairs with the signature", replid, privateReplID)
	}

	lt := dispatchOn(sess, buildLongTermIdFromId(id), logonH)
	p = ropOK(t, lt, ropLongTermIdFromId, "LongTermIdFromId(mapping signature)")
	guid, err := p.GUID()
	if err != nil {
		t.Fatal(err)
	}
	if guid != sig {
		t.Errorf("LongTermIdFromId GUID = %v, want the mapping signature %v", guid, sig)
	}
}
