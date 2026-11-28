package rop

import (
	"slices"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// buildReloadCachedInfo builds a RopReloadCachedInformation request (Reserved u16).
func buildReloadCachedInfo(inIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropReloadCachedInfo)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint16(0) // Reserved
	return b.Bytes()
}

// buildGetMessageStatus builds a RopGetMessageStatus request (MessageId).
func buildGetMessageStatus(inIdx uint8, msgEID uint64) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropGetMessageStatus)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint64(msgEID)
	return b.Bytes()
}

// buildSetMessageStatus builds a RopSetMessageStatus request (MessageId, flags, mask).
func buildSetMessageStatus(inIdx uint8, msgEID uint64, status, mask uint32) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSetMessageStatus)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint64(msgEID)
	b.Uint32(status)
	b.Uint32(mask)
	return b.Bytes()
}

// buildCreateMessageAssoc builds a RopCreateMessage request with the
// AssociatedFlag set, to create a folder-associated (FAI) message.
func buildCreateMessageAssoc(inIdx, outIdx uint8, folderEID uint64) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropCreateMessage)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint16(0) // Cpid
	b.Uint64(folderEID)
	b.Uint8(1) // AssociatedFlag: FAI
	return b.Bytes()
}

// readTypedString decodes a TYPED_STRING: a type byte then, for UNICODE, the
// string body; EMPTY/NONE carry no body.
func readTypedString(t *testing.T, p *ext.Pull) string {
	t.Helper()
	typ := mustU8(t, p, "string_type")
	if typ != stringTypeUnicode {
		return ""
	}
	s, err := p.Unicode()
	if err != nil {
		t.Fatalf("typed string body: %v", err)
	}
	return s
}

// TestReloadCachedInformation opens a seeded message and reloads its cached
// header, confirming the normalized subject round-trips, recipients decode, and a
// null/non-message handle is refused cleanly.
func TestReloadCachedInformation(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "RELOADME"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	wantEC(t, dispatchOn(sess, buildReloadCachedInfo(0), 0xFFFFFFFF), ropReloadCachedInfo, ecError, "ReloadCachedInformation(null handle)")

	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]

	rc := dispatchOn(sess, buildReloadCachedInfo(0), msgH)
	p := ropOK(t, rc, ropReloadCachedInfo, "ReloadCachedInformation")
	mustU8(t, p, "HasNamedProperties")
	if prefix := readTypedString(t, p); prefix != "" {
		t.Errorf("reloaded SubjectPrefix = %q, want empty", prefix)
	}
	if subj := readTypedString(t, p); subj != "RELOADME" {
		t.Errorf("reloaded normalized subject = %q, want RELOADME", subj)
	}
	if cnt := mustU16(t, p, "RecipientCount"); cnt != 1 {
		t.Errorf("RecipientCount = %d, want 1 (the To recipient)", cnt)
	}
	cols, err := p.PropTags()
	if err != nil {
		t.Fatalf("RecipientColumns: %v", err)
	}
	if slices.ContainsFunc(cols, func(c mapi.PropTag) bool { return slices.Contains(recipientRowFields, c) }) {
		t.Errorf("RecipientColumns %v repeat a flag-driven field", cols)
	}
	if rows := mustU8(t, p, "RowCount"); rows != 1 {
		t.Errorf("RowCount = %d, want 1", rows)
	}
	mustU8(t, p, "recipientType")
	rbag := pullOpenRecipientRest(t, p, cols, "recipient")
	if got := stringProp(rbag, mapi.PrEmailAddress); got != "alice@hermex.test" {
		t.Errorf("recipient email = %q, want alice@hermex.test", got)
	}
}

// TestMessageStatusViaFolder drives RopGetMessageStatus / RopSetMessageStatus
// against a folder handle and verifies initial status, masked merge, in-conflict
// refusal, missing message refusal, and non-folder handle refusal without state mutation.
func TestMessageStatusViaFolder(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "STATUS"))
	msgEID := uint64(mapi.MakeEIDEx(1, mid))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	folderH := dispatchOK(t, sess, buildOpenFolder(0, 1, inboxEID), ropOpenFolder, "OpenFolder", logonH, 0xFFFFFFFF)[1]

	getStatus := func(label string) uint32 {
		t.Helper()
		resp := dispatchOn(sess, buildGetMessageStatus(0, msgEID), folderH)
		p := ropOK(t, resp, ropGetMessageStatus, label)
		return mustU32(t, p, label+" status")
	}
	setStatus := func(label string, status, mask uint32) uint32 {
		t.Helper()
		resp := dispatchOn(sess, buildSetMessageStatus(0, msgEID, status, mask), folderH)
		p := ropOK(t, resp, ropSetMessageStatus, label)
		return mustU32(t, p, label+" status")
	}

	if s := getStatus("initial GetMessageStatus"); s != 0 {
		t.Fatalf("initial GetMessageStatus = %#x, want 0", s)
	}
	if s := setStatus("SetMessageStatus(HIDDEN)", 0x4, 0x4); s != 0x4 {
		t.Fatalf("SetMessageStatus(HIDDEN) = %#x, want 0x4", s)
	}
	if s := setStatus("SetMessageStatus(ANSWERED)", 0x200, 0x200); s != 0x204 {
		t.Fatalf("SetMessageStatus(ANSWERED) = %#x, want 0x204 (HIDDEN preserved)", s)
	}
	if s := getStatus("GetMessageStatus after merges"); s != 0x204 {
		t.Errorf("GetMessageStatus after merges = %#x, want 0x204", s)
	}

	// Hostile cases: in-conflict bit, missing message on both get and set, and non-folder handle.
	inConflict := dispatchOn(sess, buildSetMessageStatus(0, msgEID, msgStatusInConflict, msgStatusInConflict), folderH)
	wantEC(t, inConflict, ropSetMessageStatus, ecAccessDenied, "SetMessageStatus(IN_CONFLICT)")
	if s := getStatus("GetMessageStatus after the refused set"); s != 0x204 {
		t.Errorf("GetMessageStatus after the refused IN_CONFLICT set = %#x, want 0x204 unchanged", s)
	}
	missingEID := uint64(mapi.MakeEIDEx(1, 999999))
	wantEC(t, dispatchOn(sess, buildGetMessageStatus(0, missingEID), folderH), ropGetMessageStatus, ecNotFound, "GetMessageStatus(missing)")
	wantEC(t, dispatchOn(sess, buildSetMessageStatus(0, missingEID, 0x4, 0x4), folderH), ropSetMessageStatus, ecNotFound, "SetMessageStatus(missing)")
	wantEC(t, dispatchOn(sess, buildGetMessageStatus(0, msgEID), logonH), ropGetMessageStatus, ecNotSupported, "GetMessageStatus via a non-folder handle")
	wantEC(t, dispatchOn(sess, buildSetMessageStatus(0, msgEID, 0x4, 0x4), logonH), ropSetMessageStatus, ecNotSupported, "SetMessageStatus via a non-folder handle")
}

// TestCreateAssociatedMessage drives RopCreateMessage with the associated flag
// and proves the message appears only in associated (FAI) content syncs.
func TestCreateAssociatedMessage(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	inboxFID := int64(mapi.PrivateFIDInbox)

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store

	msgH := dispatchOK(t, sess, buildCreateMessageAssoc(0, 1, inboxEID), ropCreateMessage, "CreateMessage(assoc)", logonH, 0xFFFFFFFF)[1]
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "HIDDEN-RULE"}}), ropSetProperties, "SetProperties(assoc)", msgH)
	sc := dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, msgH)
	mid := uint64(mapi.EID(saveChangesEID(t, sc)).GCValue())

	normal, err := store.GetContentSync(objectstore.ContentSyncRequest{
		FolderID: inboxFID, Given: looseSet(), Seen: looseSet(), SeenFAI: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if containsMID(normal.ChangedMIDs, mid) {
		t.Errorf("FAI message appeared in a normal content sync: %v", normal.ChangedMIDs)
	}
	assoc, err := store.GetContentSync(objectstore.ContentSyncRequest{
		FolderID: inboxFID, Given: looseSet(), Seen: nil, SeenFAI: looseSet(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsMID(assoc.ChangedMIDs, mid) {
		t.Errorf("FAI message missing from an associated content sync: %v", assoc.ChangedMIDs)
	}
}
