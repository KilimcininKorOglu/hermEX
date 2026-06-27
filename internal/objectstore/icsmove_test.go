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

	mid, err := s.CreateMessage(src, richMsg("move me"))
	if err != nil {
		t.Fatal(err)
	}
	origCN := msgCN(t, s, mid)
	orig, err := s.OpenMessage(mid)
	if err != nil {
		t.Fatal(err)
	}
	dstMID := mid + 0x100000 // an unused, client-chosen destination id

	assoc, err := s.MoveMessageImport(src, mid, dst, dstMID)
	if err != nil {
		t.Fatalf("MoveMessageImport: %v", err)
	}
	if assoc {
		t.Errorf("normal message reported associated")
	}

	// The source id is gone; the destination id lives in the destination folder.
	if n := countRows(t, s, `SELECT COUNT(*) FROM messages WHERE message_id=?`, mid); n != 0 {
		t.Errorf("source id %#x still present (%d rows)", mid, n)
	}
	var (
		parent, isAssoc int64
		newCN           int64
	)
	if err := s.objdb.QueryRow(
		`SELECT parent_fid, is_associated, change_number FROM messages WHERE message_id=?`,
		dstMID).Scan(&parent, &isAssoc, &newCN); err != nil {
		t.Fatalf("destination id %#x not found: %v", dstMID, err)
	}
	if parent != dst {
		t.Errorf("parent_fid = %d, want %d (not relocated)", parent, dst)
	}
	if isAssoc != 0 {
		t.Errorf("is_associated = %d, want 0", isAssoc)
	}
	if uint64(newCN) == origCN {
		t.Errorf("change_number unchanged (%#x); a move must mint a fresh one", origCN)
	}

	// The property bag followed the id rename rather than being orphaned or dropped.
	if n := countRows(t, s, `SELECT COUNT(*) FROM message_properties WHERE message_id=?`, mid); n != 0 {
		t.Errorf("%d property rows left under the old id", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM message_properties WHERE message_id=?`, dstMID); n == 0 {
		t.Errorf("no property rows under the new id; cascade lost them")
	}
	// The time index keys on (folder_id, message_id); its parent column must point at
	// the destination, not the stale source folder.
	var tfid int64
	if err := s.objdb.QueryRow(
		`SELECT folder_id FROM msgtime_index WHERE message_id=?`, dstMID).Scan(&tfid); err != nil {
		t.Fatalf("time-index row missing for %#x: %v", dstMID, err)
	}
	if tfid != dst {
		t.Errorf("time-index folder_id = %d, want %d", tfid, dst)
	}

	got, err := s.OpenMessage(dstMID)
	if err != nil {
		t.Fatal(err)
	}
	assertMessageEqual(t, "moved message", orig, got)
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
