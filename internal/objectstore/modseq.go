package objectstore

import "database/sql"

// CONDSTORE/QRESYNC (RFC 7162) modification sequences. modseq is an IMAP-local
// monotonic counter held in the index (one sequence space per folder); it is
// independent of the MAPI/ICS change-number so an IMAP flag change advances MODSEQ
// without perturbing what a MAPI client sees as changed.

// nextModSeq advances a folder's modseq counter within tx and returns the new
// value. The read-then-write shares the caller's transaction so concurrent
// allocations cannot collide on a value.
func nextModSeq(tx *sql.Tx, folderID int64) (int64, error) {
	var ms int64
	if err := tx.QueryRow(`SELECT highest_modseq FROM folders WHERE folder_id=?`, folderID).Scan(&ms); err != nil {
		return 0, err
	}
	ms++
	if _, err := tx.Exec(`UPDATE folders SET highest_modseq=? WHERE folder_id=?`, ms, folderID); err != nil {
		return 0, err
	}
	return ms, nil
}

// MessageModSeqs returns each live message's IMAP modification sequence, keyed by
// UID (CONDSTORE, RFC 7162).
func (s *Store) MessageModSeqs(folderID int64) (map[uint32]uint64, error) {
	rows, err := s.idxdb.Query(`SELECT uid, modseq FROM messages WHERE folder_id=?`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint32]uint64{}
	for rows.Next() {
		var uid, ms int64
		if err := rows.Scan(&uid, &ms); err != nil {
			return nil, err
		}
		out[uint32(uid)] = uint64(ms)
	}
	return out, rows.Err()
}

// FolderHighestModSeq returns a folder's current highest modification sequence,
// the value reported as HIGHESTMODSEQ. A folder with no index row reports 0.
func (s *Store) FolderHighestModSeq(folderID int64) (uint64, error) {
	var ms int64
	err := s.idxdb.QueryRow(`SELECT highest_modseq FROM folders WHERE folder_id=?`, folderID).Scan(&ms)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return uint64(ms), nil
}
