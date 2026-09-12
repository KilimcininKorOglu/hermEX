package objectstore

// FolderContentSignal reports a folder's content state as two numbers: how many
// messages it holds and the highest modification counter any of them carries.
// The pair is a change detector, not an inventory: a create or a delete moves the
// count, and an edit or a read-state change moves the counter, so a caller that
// remembers the pair can tell that the folder's messages moved without reading a
// row per message. It is the cheap counterpart to FolderMessageChangeNumbers,
// which a caller uses when it needs to know *which* message changed.
//
// softDeleted selects the Recoverable Items side of the folder (the same rows
// ListSoftDeletedIDs returns) instead of the live one. Associated (FAI) messages
// are excluded on both sides, because they are not what a contents listing shows.
func (s *Store) FolderContentSignal(folderID int64, softDeleted bool) (count int, marker uint64, err error) {
	deleted := 0
	if softDeleted {
		deleted = 1
	}
	var n int
	var cn int64
	err = s.objdb.QueryRow(
		`SELECT COUNT(*), COALESCE(MAX(MAX(change_number, COALESCE(read_cn, 0))), 0) FROM messages
		 WHERE parent_fid=? AND is_deleted=? AND is_associated=0`, folderID, deleted).Scan(&n, &cn)
	if err != nil {
		return 0, 0, err
	}
	// #nosec G115 -- a change number crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	return n, uint64(cn), nil
}

// FolderChildSignal reports a folder's direct child state as two numbers: how
// many live child folders it has and the highest change number among them. Like
// FolderContentSignal it is a change detector: a child created or moved in
// carries a fresh change number, and a child deleted or moved out moves the
// count.
//
// A rename does not move either number, because a display-name write leaves the
// folder row's change number untouched. A caller that must observe renames needs
// a different source.
func (s *Store) FolderChildSignal(parentID int64) (count int, marker uint64, err error) {
	var n int
	var cn int64
	err = s.objdb.QueryRow(
		`SELECT COUNT(*), COALESCE(MAX(change_number), 0) FROM folders
		 WHERE parent_id=? AND is_deleted=0 AND is_search=0`, parentID).Scan(&n, &cn)
	if err != nil {
		return 0, 0, err
	}
	// #nosec G115 -- a change number crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	return n, uint64(cn), nil
}
