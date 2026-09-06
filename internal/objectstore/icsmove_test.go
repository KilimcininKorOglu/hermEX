package objectstore

import (
	"errors"
	"testing"

	"hermex/internal/mapi"
)

// TestMoveMessageImport drives the store side of RopSynchronizationImportMessageMove:
// a message relocates from its source folder into the destination folder under the
// client-supplied id, with a fresh change number, while every child row follows the
// id rename through ON UPDATE CASCADE and the time index is repointed at the new
// parent. The source id disappears and the content survives byte for byte.
func TestMoveMessageImport(t *testing.T) {
	s := openSeededStore(t)
	src := int64(mapi.PrivateFIDInbox)
	dst := int64(mapi.PrivateFIDSentItems)

	mid := mustCreateMessage(t, s, src, richMsg("move me"))
	origCN := msgCN(t, s, mid)
	orig := mustOpenMessage(t, s, mid)
	dstMID := mid + 0x100000 // an unused, client-chosen destination id

	assoc, err := s.MoveMessageImport(src, mid, dst, dstMID)
	mustNoErr(t, "move message import", err)
	wantEq(t, "normal message reported associated", assoc, false)

	// The source id is gone; the destination id lives in the destination folder.
	wantRows(t, s, "rows left under the source id", 0, `SELECT COUNT(*) FROM messages WHERE message_id=?`, mid)
	var parent, isAssoc, newCN int64
	mustScan(t, s.objdb.QueryRow(
		`SELECT parent_fid, is_associated, change_number FROM messages WHERE message_id=?`, dstMID),
		&parent, &isAssoc, &newCN)
	wantEq(t, "parent_fid after the move", parent, dst)
	wantEq(t, "is_associated", isAssoc, int64(0))
	wantNotEq(t, "change_number (a move must mint a fresh one)", uint64(newCN), origCN)

	// The property bag followed the id rename rather than being orphaned or dropped.
	wantRows(t, s, "property rows left under the old id", 0, `SELECT COUNT(*) FROM message_properties WHERE message_id=?`, mid)
	wantNotEq(t, "property rows under the new id (cascade)",
		countRows(t, s, `SELECT COUNT(*) FROM message_properties WHERE message_id=?`, dstMID), 0)

	// The time index keys on (folder_id, message_id); its parent column must point at
	// the destination, not the stale source folder.
	var tfid int64
	mustScan(t, s.objdb.QueryRow(`SELECT folder_id FROM msgtime_index WHERE message_id=?`, dstMID), &tfid)
	wantEq(t, "time-index folder_id", tfid, dst)

	assertMessageEqual(t, "moved message", orig, mustOpenMessage(t, s, dstMID))
}

// TestMoveMessageImportAssociated proves the FAI flag rides through the move: an
// associated source becomes an associated destination and the primitive reports it.
func TestMoveMessageImportAssociated(t *testing.T) {
	s := openSeededStore(t)
	src := int64(mapi.PrivateFIDInbox)
	dst := int64(mapi.PrivateFIDSentItems)

	mid, err := s.CreateMessage(src, richMsg("fai move"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.objdb.Exec(`UPDATE messages SET is_associated=1 WHERE message_id=?`, mid); err != nil {
		t.Fatal(err)
	}
	dstMID := mid + 0x100000

	assoc, err := s.MoveMessageImport(src, mid, dst, dstMID)
	if err != nil {
		t.Fatalf("MoveMessageImport: %v", err)
	}
	if !assoc {
		t.Errorf("moved FAI message reported non-associated")
	}
	var isAssoc int64
	if err := s.objdb.QueryRow(`SELECT is_associated FROM messages WHERE message_id=?`, dstMID).Scan(&isAssoc); err != nil {
		t.Fatal(err)
	}
	if isAssoc != 1 {
		t.Errorf("is_associated = %d after move, want 1 (FAI flag lost)", isAssoc)
	}
}

// TestMoveMessageImportObjectDeleted asserts that moving a source the store no longer
// holds yields ErrObjectDeleted (the SYNC_E_OBJECT_DELETED the handler maps), rather
// than a generic failure or a silent no-op.
func TestMoveMessageImportObjectDeleted(t *testing.T) {
	s := openSeededStore(t)
	src := int64(mapi.PrivateFIDInbox)
	dst := int64(mapi.PrivateFIDSentItems)

	_, err := s.MoveMessageImport(src, 0x7654321, dst, 0x100000)
	if !errors.Is(err, ErrObjectDeleted) {
		t.Fatalf("MoveMessageImport of an absent source: err = %v, want ErrObjectDeleted", err)
	}
}
