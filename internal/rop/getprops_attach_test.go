package rop

import (
	"slices"
	"testing"

	"hermex/internal/mapi"
)

// TestGetPropertiesOnAnOpenAttachment proves RopGetPropertiesSpecific and
// RopGetPropertiesAll read an opened attachment ([MS-OXCPRPT] 2.2.2.9 and
// 2.2.2.10 apply to attachment objects) and refuse invalid handles cleanly.
func TestGetPropertiesOnAnOpenAttachment(t *testing.T) {
	dir := provisionedDir(t)
	msgID := seedAttachmentMessage(t, dir)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(msgID)))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	attachH := dispatchOK(t, sess, buildOpenAttachment(0, 1, 0), ropOpenAttachment, "OpenAttachment", msgH, 0xFFFFFFFF)[1]

	cols := []mapi.PropTag{mapi.PrAttachLongFilename}
	gps := dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), attachH)
	p := ropOK(t, gps, ropGetPropertiesSpecific, "GetPropertiesSpecific(attachment)")
	wantProp(t, decodeRow(t, p, cols), mapi.PrAttachLongFilename, "a.bin", "attachment filename")

	gpa := dispatchOn(sess, buildGetProps(ropGetPropertiesAll, 0, nil), attachH)
	p = ropOK(t, gpa, ropGetPropertiesAll, "GetPropertiesAll(attachment)")
	all, err := p.PropertyValues()
	if err != nil {
		t.Fatalf("decode TPROPVAL_ARRAY: %v", err)
	}
	wantProp(t, all, mapi.PrAttachLongFilename, "a.bin", "GetPropertiesAll filename")

	// The record key is computed; the opened attachment reports the key its
	// attachment table row carries.
	keyCols := []mapi.PropTag{mapi.PrRecordKey}
	gk := dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, keyCols), attachH)
	p = ropOK(t, gk, ropGetPropertiesSpecific, "GetPropertiesSpecific(record key)")
	v := rowProp(t, decodeRow(t, p, keyCols), mapi.PrRecordKey, "attachment record key")
	key, ok := v.([]byte)
	if !ok {
		t.Fatalf("attachment record key = %T, want []byte", v)
	}
	if !slices.Equal(key, attachmentRecordKey(0)) {
		t.Errorf("attachment record key = %x, want %x", key, attachmentRecordKey(0))
	}

	// Hostile: null handle returns ecError on GetPropertiesSpecific without affecting attachH.
	wantEC(t, dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), 0xFFFFFFFF), ropGetPropertiesSpecific, ecError, "GetPropertiesSpecific(null handle)")
}

// TestCopiedAttachmentStoresNoRecordKey proves CopyTo from an opened attachment
// does not carry its computed record key into the destination, where it would be
// saved and name the source's position instead of the destination's.
func TestCopiedAttachmentStoresNoRecordKey(t *testing.T) {
	dir := provisionedDir(t)
	msgID := seedAttachmentMessage(t, dir)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(msgID)))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	srcH := dispatchOK(t, sess, buildOpenAttachment(0, 1, 0), ropOpenAttachment, "OpenAttachment", msgH, 0xFFFFFFFF)[1]
	_, dstH := createAttachmentNum(t, sess, msgH)

	dispatchOK(t, sess, buildCopyTo(0, 1, 0, nil), ropCopyTo, "CopyTo(attachment)", srcH, dstH)
	dispatchOK(t, sess, buildSaveChangesAttachment(0, 1), ropSaveChangesAttachment, "SaveChangesAttachment", msgH, dstH)

	stored, err := store.GetAttachmentProperties(sess.get(dstH).attachW.attachmentID)
	if err != nil {
		t.Fatal(err)
	}
	wantProp(t, stored, mapi.PrAttachLongFilename, "a.bin", "copied filename")
	if v, ok := stored.Get(mapi.PrRecordKey); ok {
		t.Errorf("copied attachment stored record key %x", v)
	}
}

// TestGetPropertiesListNamesEveryTag proves RopGetPropertiesList answers the
// tags an object holds ([MS-OXCPRPT] 2.2.2.12) on a message, an attachment and
// a folder, and refuses null handles with ecError.
func TestGetPropertiesListNamesEveryTag(t *testing.T) {
	dir := provisionedDir(t)
	msgID := seedAttachmentMessage(t, dir)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(msgID)))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	attachH := dispatchOK(t, sess, buildOpenAttachment(0, 1, 0), ropOpenAttachment, "OpenAttachment", msgH, 0xFFFFFFFF)[1]
	folderH := dispatchOK(t, sess, buildOpenFolder(0, 1, inboxEID), ropOpenFolder, "OpenFolder", logonH, 0xFFFFFFFF)[1]

	for _, c := range []struct {
		name   string
		handle uint32
		want   mapi.PropTag
	}{
		{"message", msgH, mapi.PrSubject},
		{"attachment", attachH, mapi.PrAttachLongFilename},
		{"folder", folderH, mapi.PrDisplayName},
	} {
		out := dispatchOn(sess, []byte{ropGetPropertiesList, 0, 0}, c.handle)
		p := ropOK(t, out, ropGetPropertiesList, c.name+" GetPropertiesList")
		tags, err := p.PropTags()
		if err != nil {
			t.Fatalf("%s: decode tags: %v", c.name, err)
		}
		if !slices.Contains(tags, c.want) {
			t.Errorf("%s tags %v lack %v", c.name, tags, c.want)
		}
		wantDrained(t, p, c.name+" GetPropertiesList")
	}

	wantEC(t, dispatchOn(sess, []byte{ropGetPropertiesList, 0, 0}, 0xFFFFFFFF), ropGetPropertiesList, ecError, "GetPropertiesList(null handle)")
}

// TestGetPropertiesOnACreatedAttachment proves a created attachment reads back
// its buffered edits before RopSaveChangesAttachment, on a stored parent and on
// a message still being composed.
func TestGetPropertiesOnACreatedAttachment(t *testing.T) {
	dir := provisionedDir(t)
	msgID := seedInboxMessage(t, dir, "parent")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	storedParent := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(msgID)))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	composeParent := dispatchOK(t, sess, buildCreateMessage(0, 1, inboxEID), ropCreateMessage, "CreateMessage", logonH, 0xFFFFFFFF)[1]

	for name, parent := range map[string]uint32{"stored": storedParent, "compose": composeParent} {
		ca, h := sess.Dispatch(buildCreateAttachment(0, 1), []uint32{parent, 0xFFFFFFFF})
		ropOK(t, ca, ropCreateAttachment, name+" CreateAttachment")
		attachH := h[1]
		dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrAttachLongFilename, Value: "new.txt"}}), ropSetProperties, name+" SetProperties", attachH)

		cols := []mapi.PropTag{mapi.PrAttachLongFilename, mapi.PrRenderingPosition}
		gps := dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), attachH)
		row := decodeRow(t, ropOK(t, gps, ropGetPropertiesSpecific, name+" GetPropertiesSpecific"), cols)
		wantProp(t, row, mapi.PrAttachLongFilename, "new.txt", name+" buffered filename")
		wantProp(t, row, mapi.PrRenderingPosition, int32(-1), name+" opening property")
	}
}

// TestGetPropertiesOnAComposedMessage proves a message being composed reads back
// what SetProperties gave it before its first save.
func TestGetPropertiesOnAComposedMessage(t *testing.T) {
	dir := provisionedDir(t)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgH := dispatchOK(t, sess, buildCreateMessage(0, 1, inboxEID), ropCreateMessage, "CreateMessage", logonH, 0xFFFFFFFF)[1]
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "draft"}}), ropSetProperties, "SetProperties(compose)", msgH)

	cols := []mapi.PropTag{mapi.PrSubject}
	gps := dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), msgH)
	wantProp(t, decodeRow(t, ropOK(t, gps, ropGetPropertiesSpecific, "GetPropertiesSpecific(compose)"), cols), mapi.PrSubject, "draft", "compose subject")
}
