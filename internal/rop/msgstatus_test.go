package rop

import (
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
// header, confirming the normalized subject round-trips and the response carries
// the same empty recipient table shape RopOpenMessage emits.
func TestReloadCachedInformation(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "RELOADME"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]

	rc, _ := sess.Dispatch(buildReloadCachedInfo(0), []uint32{msgH})
	p := ext.NewPull(rc, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropReloadCachedInfo {
		t.Fatalf("ReloadCachedInformation RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("ReloadCachedInformation ReturnValue = %#x", ec)
	}
	mustU8(t, p, "HasNamedProperties")
	readTypedString(t, p) // SubjectPrefix
	if subj := readTypedString(t, p); subj != "RELOADME" {
		t.Errorf("reloaded normalized subject = %q, want RELOADME", subj)
	}
	if rc := mustU16(t, p, "RecipientCount"); rc != 1 {
		t.Errorf("RecipientCount = %d, want 1 (the To recipient)", rc)
	}
	cols, err := p.PropTags()
	if err != nil {
		t.Fatalf("RecipientColumns: %v", err)
	}
	if len(cols) != 0 {
		t.Errorf("RecipientColumns = %d tags, want 0", len(cols))
	}
	if rows := mustU8(t, p, "RowCount"); rows != 1 {
		t.Errorf("RowCount = %d, want 1", rows)
	}
	mustU8(t, p, "recipientType")
	mustU16(t, p, "codePageId")
	mustU16(t, p, "reserved")
	rbag, ok := pullRecipientRow(p, cols)
	if !ok {
		t.Fatal("recipient row decode failed")
	}
	if got := stringProp(rbag, mapi.PrEmailAddress); got != "alice@hermex.test" {
		t.Errorf("recipient email = %q, want alice@hermex.test", got)
	}
}

// TestMessageStatusViaFolder drives RopGetMessageStatus / RopSetMessageStatus
// against a FOLDER handle (the object kind they require) and a message id. It
// confirms the create-seeded zero status, the masked merge that preserves
// unmasked bits, the in-conflict rejection, the not-found cases, and that a
// non-folder handle is refused.
func TestMessageStatusViaFolder(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "STATUS"))
	msgEID := uint64(mapi.MakeEIDEx(1, mid))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]

	getStatus := func() (uint32, uint32) {
		resp, _ := sess.Dispatch(buildGetMessageStatus(0, msgEID), []uint32{folderH})
		p := ext.NewPull(resp, ext.FlagUTF16)
		mustU8(t, p, "RopId")
		mustU8(t, p, "hindex")
		ec := mustU32(t, p, "ec")
		if ec != ecSuccess {
			return 0, ec
		}
		return mustU32(t, p, "status"), ec
	}
	setStatus := func(status, mask uint32) (uint32, uint32) {
		resp, _ := sess.Dispatch(buildSetMessageStatus(0, msgEID, status, mask), []uint32{folderH})
		p := ext.NewPull(resp, ext.FlagUTF16)
		mustU8(t, p, "RopId")
		mustU8(t, p, "hindex")
		ec := mustU32(t, p, "ec")
		if ec != ecSuccess {
			return 0, ec
		}
		return mustU32(t, p, "status"), ec
	}

	// Create-seeded status is 0.
	if s, ec := getStatus(); ec != ecSuccess || s != 0 {
		t.Fatalf("initial GetMessageStatus = %#x (ec %#x), want 0", s, ec)
	}
	// Set HIDDEN (0x4): merged with original 0 → 0x4.
	if s, ec := setStatus(0x4, 0x4); ec != ecSuccess || s != 0x4 {
		t.Fatalf("SetMessageStatus(HIDDEN) = %#x (ec %#x), want 0x4", s, ec)
	}
	// Set ANSWERED (0x200) keeping HIDDEN: merged → 0x204.
	if s, ec := setStatus(0x200, 0x200); ec != ecSuccess || s != 0x204 {
		t.Fatalf("SetMessageStatus(ANSWERED) = %#x (ec %#x), want 0x204 (HIDDEN preserved)", s, ec)
	}
	if s, _ := getStatus(); s != 0x204 {
		t.Errorf("GetMessageStatus after merges = %#x, want 0x204", s)
	}
	// Setting the in-conflict bit is refused.
	if _, ec := setStatus(msgStatusInConflict, msgStatusInConflict); ec != ecAccessDenied {
		t.Errorf("SetMessageStatus(IN_CONFLICT) ec = %#x, want ecAccessDenied", ec)
	}
	// A non-existent message reports not-found.
	if _, ec := func() (uint32, uint32) {
		resp, _ := sess.Dispatch(buildGetMessageStatus(0, uint64(mapi.MakeEIDEx(1, 999999))), []uint32{folderH})
		p := ext.NewPull(resp, ext.FlagUTF16)
		mustU8(t, p, "RopId")
		mustU8(t, p, "hindex")
		return 0, mustU32(t, p, "ec")
	}(); ec != ecNotFound {
		t.Errorf("GetMessageStatus(missing) ec = %#x, want ecNotFound", ec)
	}
	// A non-folder handle (the logon root) is refused.
	resp, _ := sess.Dispatch(buildGetMessageStatus(0, msgEID), []uint32{logonH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecNotSupported {
		t.Errorf("GetMessageStatus via non-folder handle ec = %#x, want ecNotSupported", ec)
	}
}

// TestCreateAssociatedMessage drives RopCreateMessage with the associated flag
// and proves the message is stored as folder-associated: it appears in an
// associated (FAI) content sync but not in a normal one.
func TestCreateAssociatedMessage(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	inboxFID := int64(mapi.PrivateFIDInbox)

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	store := sess.get(logonH).store

	_, h = sess.Dispatch(buildCreateMessageAssoc(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "HIDDEN-RULE"}}), []uint32{msgH})
	sc, _ := sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})
	mid := uint64(mapi.EID(saveChangesEID(t, sc)).GCValue())

	// Normal sync (SYNC_NORMAL only): the FAI message is out of scope.
	normal, err := store.GetContentSync(objectstore.ContentSyncRequest{
		FolderID: inboxFID, Given: looseSet(), Seen: looseSet(), SeenFAI: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if containsMID(normal.ChangedMIDs, mid) {
		t.Errorf("FAI message appeared in a normal content sync: %v", normal.ChangedMIDs)
	}
	// Associated sync (SYNC_ASSOCIATED): the FAI message is reported.
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
