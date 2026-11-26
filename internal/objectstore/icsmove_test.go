package objectstore

import (
	"errors"
	"testing"
	"time"

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
	wantRows(t, s, "rows created under the refused destination id", 0, `SELECT COUNT(*) FROM messages WHERE message_id=?`, 0x100000)

	// A source named under a folder it is not in is not that folder's message: the
	// client cannot move a message out of a folder it did not sync.
	mid := mustCreateMessage(t, s, int64(mapi.PrivateFIDDeletedItems), richMsg("elsewhere"))
	if _, err := s.MoveMessageImport(src, mid, dst, mid+0x100000); !errors.Is(err, ErrObjectDeleted) {
		t.Errorf("move with a source folder that does not hold the message: err = %v, want ErrObjectDeleted", err)
	}
	var parent int64
	mustScan(t, s.objdb.QueryRow(`SELECT parent_fid FROM messages WHERE message_id=?`, mid), &parent)
	wantEq(t, "the message named under the wrong folder stays put", parent, int64(mapi.PrivateFIDDeletedItems))
}

// TestMoveMessageImportRefusesAnIDHeldInAnotherFolder: the client chooses the
// destination id from its own replica range ([MS-OXCFXICS] 3.3.5.9), so an id the
// store already holds in another folder is not a retried move. Taking it would
// delete that unrelated message. The move is refused and both messages survive.
func TestMoveMessageImportRefusesAnIDHeldInAnotherFolder(t *testing.T) {
	s := openSeededStore(t)
	src := int64(mapi.PrivateFIDInbox)
	dst := int64(mapi.PrivateFIDSentItems)
	other := int64(mapi.PrivateFIDDeletedItems)

	mid := mustCreateMessage(t, s, src, richMsg("move me"))
	victim := mustCreateMessage(t, s, other, richMsg("must survive"))

	if _, err := s.MoveMessageImport(src, mid, dst, victim); !errors.Is(err, ErrMoveTargetInUse) {
		t.Fatalf("move onto an id held in another folder = %v, want ErrMoveTargetInUse", err)
	}
	var parent int64
	mustScan(t, s.objdb.QueryRow(`SELECT parent_fid FROM messages WHERE message_id=?`, victim), &parent)
	wantEq(t, "the other folder's message stays where it was", parent, other)
	mustScan(t, s.objdb.QueryRow(`SELECT parent_fid FROM messages WHERE message_id=?`, mid), &parent)
	wantEq(t, "the refused source stays in its folder", parent, src)
}

// TestMoveMessageImportReplaysIntoTheSameFolder: a retried move that re-sends a
// destination id already committed in the destination folder replaces that row.
func TestMoveMessageImportReplaysIntoTheSameFolder(t *testing.T) {
	s := openSeededStore(t)
	src := int64(mapi.PrivateFIDInbox)
	dst := int64(mapi.PrivateFIDSentItems)

	first := mustCreateMessage(t, s, src, richMsg("first"))
	dstMID := first + 0x100000
	_, err := s.MoveMessageImport(src, first, dst, dstMID)
	mustNoErr(t, "first move", err)

	second := mustCreateMessage(t, s, src, richMsg("second"))
	_, err = s.MoveMessageImport(src, second, dst, dstMID)
	mustNoErr(t, "replayed move under the committed id", err)
	wantRows(t, s, "rows under the destination id", 1, `SELECT COUNT(*) FROM messages WHERE message_id=? AND parent_fid=?`, dstMID, dst)
}

// TestMoveMessageImportKeepsMailVisibleToIMAP proves a mail message Outlook moves
// through ICS stays visible to IMAP, POP3 and webmail in the destination folder:
// the move is one message changing folders (RFC 3501 sees it leave one mailbox
// and arrive in another), so the destination lists it with its \Seen flag and
// serves the same bytes, and the source no longer lists it.
func TestMoveMessageImportKeepsMailVisibleToIMAP(t *testing.T) {
	s := openSeededStore(t)
	src := int64(mapi.PrivateFIDInbox)
	dst := int64(mapi.PrivateFIDSentItems)
	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: moved\r\n" +
		"Date: Thu, 01 Jan 2026 09:00:00 +0000\r\nMessage-ID: <moved-1@example.test>\r\n\r\nBody.\r\n")
	info, err := s.AppendMessage(src, raw, time.Unix(1767258000, 0), FlagSeen)
	mustNoErr(t, "append", err)
	before, err := s.GetMessageRaw(src, info.UID)
	mustNoErr(t, "raw before the move", err)
	dstMID := info.ID + 0x100000

	_, err = s.MoveMessageImport(src, info.ID, dst, dstMID)
	mustNoErr(t, "move message import", err)

	wantEq(t, "messages left in the source", len(mustListMessages(t, s, src)), 0)
	moved := mustListMessages(t, s, dst)
	if len(moved) != 1 || moved[0].ID != dstMID {
		t.Fatalf("destination lists %+v, want the moved message %d", moved, dstMID)
	}
	wantEq(t, "\\Seen after the move", mustMessageFlags(t, s, dst, moved[0].UID)&FlagSeen, int64(FlagSeen))
	after, err := s.GetMessageRaw(dst, moved[0].UID)
	mustNoErr(t, "raw after the move", err)
	wantEq(t, "served bytes after the move", string(after), string(before))
	wantEq(t, "recorded size", moved[0].Size, int64(len(after)))
}
