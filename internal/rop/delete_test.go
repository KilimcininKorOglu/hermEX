package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// buildDeletePropsOp builds a RopDeleteProperties (or NoReplicate) request: a
// PROPTAG_ARRAY of the tags to remove.
func buildDeletePropsOp(ropID, inIdx uint8, tags []mapi.PropTag) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropID)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	_ = b.PropTags(tags)
	return b.Bytes()
}

// TestDeleteMessageProperties drives RopDeleteProperties on an opened message: a
// property set this session and one from the seed are deleted, a third is set then
// deleted before any save (so the delete must win over the buffered set), and the
// message is saved. It proves the removals reach the store, that a delete-after-set
// does not leak the set, that the empty problem array is returned, that the
// NoReplicate variant behaves identically, and that the delete advances the change
// number so ICS observes the edit.
func TestDeleteMessageProperties(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	inboxFID := int64(mapi.PrivateFIDInbox)
	mid := uint64(seedInboxMessage(t, dir, "DELME"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	store := sess.get(logonH).store

	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]

	// Set PrImportance and save, so there is a persisted property to delete. Record
	// the change number after this save to isolate the delete's own bump.
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrImportance, Value: int32(2)}}), []uint32{msgH})
	sc, _ := sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})
	saveChangesEID(t, sc)
	before, _ := store.GetMessageProperties(int64(mid), mapi.PrSubject, mapi.PrImportance)
	if _, ok := before.Get(mapi.PrImportance); !ok {
		t.Fatal("PrImportance missing before delete")
	}
	syncAfterSet, err := store.GetContentSync(objectstore.ContentSyncRequest{FolderID: inboxFID, Given: looseSet(mid), Seen: looseSet()})
	if err != nil {
		t.Fatal(err)
	}
	cnSet := syncAfterSet.LastCN

	// Set PrSensitivity (buffered, unsaved), then delete subject + importance +
	// sensitivity in one call: the sensitivity delete must override its buffered set.
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSensitivity, Value: int32(1)}}), []uint32{msgH})
	dp, _ := sess.Dispatch(buildDeletePropsOp(ropDeleteProperties, 0, []mapi.PropTag{mapi.PrSubject, mapi.PrImportance, mapi.PrSensitivity}), []uint32{msgH})
	p := ext.NewPull(dp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropDeleteProperties {
		t.Fatalf("DeleteProperties RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("DeleteProperties ReturnValue = %#x", ec)
	}
	if pc := mustU16(t, p, "problemCount"); pc != 0 {
		t.Errorf("DeleteProperties PropertyProblemCount = %d, want 0", pc)
	}
	sc2, _ := sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})
	saveChangesEID(t, sc2)

	// All three are gone from the store; the buffered sensitivity set did not leak.
	after, _ := store.GetMessageProperties(int64(mid), mapi.PrSubject, mapi.PrImportance, mapi.PrSensitivity)
	for _, tag := range []mapi.PropTag{mapi.PrSubject, mapi.PrImportance, mapi.PrSensitivity} {
		if _, ok := after.Get(tag); ok {
			t.Errorf("property %s survived DeleteProperties", tag)
		}
	}

	// The delete advanced the change number beyond the set-save.
	post, err := store.GetContentSync(objectstore.ContentSyncRequest{FolderID: inboxFID, Given: looseSet(mid), Seen: looseSet(cnSet)})
	if err != nil {
		t.Fatal(err)
	}
	if post.LastCN <= cnSet {
		t.Errorf("DeleteProperties did not advance the change number: %d -> %d", cnSet, post.LastCN)
	}

	// The NoReplicate variant is accepted and reports success (nothing left to delete).
	nr, _ := sess.Dispatch(buildDeletePropsOp(ropDeletePropertiesNoReplicate, 0, []mapi.PropTag{mapi.PrSubject}), []uint32{msgH})
	p = ext.NewPull(nr, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropDeletePropertiesNoReplicate {
		t.Fatalf("DeletePropertiesNoReplicate RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Errorf("DeletePropertiesNoReplicate ReturnValue = %#x", ec)
	}
}
