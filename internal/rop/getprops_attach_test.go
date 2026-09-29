package rop

import (
	"slices"
	"testing"

	"hermex/internal/mapi"
)

// TestGetPropertiesOnAnOpenAttachment proves RopGetPropertiesSpecific and
// RopGetPropertiesAll read an opened attachment ([MS-OXCPRPT] 2.2.2.9 and
// 2.2.2.10 apply to attachment objects), since a client reads an attachment's
// name and size off its handle after RopOpenAttachment.
func TestGetPropertiesOnAnOpenAttachment(t *testing.T) {
	dir := t.TempDir()
	msgID := seedAttachmentMessage(t, dir)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(msgID)))), []uint32{h[0], 0xFFFFFFFF})
	_, h = sess.Dispatch(buildOpenAttachment(0, 1, 0), []uint32{h[1], 0xFFFFFFFF})
	attachH := h[1]

	cols := []mapi.PropTag{mapi.PrAttachLongFilename}
	gps, _ := sess.Dispatch(buildGetProps(ropGetPropertiesSpecific, 0, cols), []uint32{attachH})
	p := ropOK(t, gps, ropGetPropertiesSpecific, "GetPropertiesSpecific(attachment)")
	wantProp(t, decodeRow(t, p, cols), mapi.PrAttachLongFilename, "a.bin", "attachment filename")

	gpa, _ := sess.Dispatch(buildGetProps(ropGetPropertiesAll, 0, nil), []uint32{attachH})
	p = ropOK(t, gpa, ropGetPropertiesAll, "GetPropertiesAll(attachment)")
	all, err := p.PropertyValues()
	if err != nil {
		t.Fatalf("decode TPROPVAL_ARRAY: %v", err)
	}
	wantProp(t, all, mapi.PrAttachLongFilename, "a.bin", "GetPropertiesAll filename")

	// The record key is computed; the opened attachment reports the key its
	// attachment table row carries.
	keyCols := []mapi.PropTag{mapi.PrRecordKey}
	gk, _ := sess.Dispatch(buildGetProps(ropGetPropertiesSpecific, 0, keyCols), []uint32{attachH})
	p = ropOK(t, gk, ropGetPropertiesSpecific, "GetPropertiesSpecific(record key)")
	v, _ := decodeRow(t, p, keyCols).Get(mapi.PrRecordKey)
	if key, _ := v.([]byte); !slices.Equal(key, attachmentRecordKey(0)) {
		t.Errorf("attachment record key = %x, want %x", v, attachmentRecordKey(0))
	}
}

// TestCopiedAttachmentStoresNoRecordKey proves CopyTo from an opened attachment
// does not carry its computed record key into the destination, where it would be
// saved and name the source's position instead of the destination's.
func TestCopiedAttachmentStoresNoRecordKey(t *testing.T) {
	dir := t.TempDir()
	msgID := seedAttachmentMessage(t, dir)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	store := sess.get(h[0]).store
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(msgID)))), []uint32{h[0], 0xFFFFFFFF})
	msgH := h[1]
	_, h = sess.Dispatch(buildOpenAttachment(0, 1, 0), []uint32{msgH, 0xFFFFFFFF})
	srcH := h[1]
	_, dstH := createAttachmentNum(t, sess, msgH)

	ct, _ := sess.Dispatch(buildCopyTo(0, 1, 0, nil), []uint32{srcH, dstH})
	ropOK(t, ct, ropCopyTo, "CopyTo(attachment)")
	sc, _ := sess.Dispatch(buildSaveChangesAttachment(0, 1), []uint32{msgH, dstH})
	ropOK(t, sc, ropSaveChangesAttachment, "SaveChangesAttachment")

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
// a folder.
func TestGetPropertiesListNamesEveryTag(t *testing.T) {
	dir := t.TempDir()
	msgID := seedAttachmentMessage(t, dir)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(msgID)))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	_, h = sess.Dispatch(buildOpenAttachment(0, 1, 0), []uint32{msgH, 0xFFFFFFFF})
	attachH := h[1]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]

	for _, c := range []struct {
		name   string
		handle uint32
		want   mapi.PropTag
	}{
		{"message", msgH, mapi.PrSubject},
		{"attachment", attachH, mapi.PrAttachLongFilename},
		{"folder", folderH, mapi.PrDisplayName},
	} {
		out, _ := sess.Dispatch([]byte{ropGetPropertiesList, 0, 0}, []uint32{c.handle})
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
}

// TestGetPropertiesOnACreatedAttachment proves a created attachment reads back
// its buffered edits before RopSaveChangesAttachment, on a stored parent and on
// a message still being composed.
func TestGetPropertiesOnACreatedAttachment(t *testing.T) {
	dir := t.TempDir()
	msgID := seedInboxMessage(t, dir, "parent")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(msgID)))), []uint32{logonH, 0xFFFFFFFF})
	storedParent := h[1]
	_, h = sess.Dispatch(buildCreateMessage(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	composeParent := h[1]

	for name, parent := range map[string]uint32{"stored": storedParent, "compose": composeParent} {
		ca, h := sess.Dispatch(buildCreateAttachment(0, 1), []uint32{parent, 0xFFFFFFFF})
		ropOK(t, ca, ropCreateAttachment, name+" CreateAttachment")
		attachH := h[1]
		sp, _ := sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrAttachLongFilename, Value: "new.txt"}}), []uint32{attachH})
		ropOK(t, sp, ropSetProperties, name+" SetProperties")

		cols := []mapi.PropTag{mapi.PrAttachLongFilename, mapi.PrRenderingPosition}
		gps, _ := sess.Dispatch(buildGetProps(ropGetPropertiesSpecific, 0, cols), []uint32{attachH})
		row := decodeRow(t, ropOK(t, gps, ropGetPropertiesSpecific, name+" GetPropertiesSpecific"), cols)
		wantProp(t, row, mapi.PrAttachLongFilename, "new.txt", name+" buffered filename")
		wantProp(t, row, mapi.PrRenderingPosition, int32(-1), name+" opening property")
	}
}

// TestGetPropertiesOnAComposedMessage proves a message being composed reads back
// what SetProperties gave it before its first save.
func TestGetPropertiesOnAComposedMessage(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	_, h = sess.Dispatch(buildCreateMessage(0, 1, inboxEID), []uint32{h[0], 0xFFFFFFFF})
	msgH := h[1]
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "draft"}}), []uint32{msgH})

	cols := []mapi.PropTag{mapi.PrSubject}
	gps, _ := sess.Dispatch(buildGetProps(ropGetPropertiesSpecific, 0, cols), []uint32{msgH})
	wantProp(t, decodeRow(t, ropOK(t, gps, ropGetPropertiesSpecific, "GetPropertiesSpecific(compose)"), cols), mapi.PrSubject, "draft", "compose subject")
}
