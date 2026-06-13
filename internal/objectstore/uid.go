package objectstore

import "fmt"

// UID and UIDVALIDITY facade over the IMAP index. The index folder row is
// created on demand (mirroring the object-store folder) the first time any of
// these is called for a folder, so a SELECT on a never-delivered folder still
// reports a stable UIDVALIDITY and a UIDNEXT of 1.

// AllocUID reserves and returns the folder's next IMAP UID, advancing the
// counter. UIDs are monotonic and never reused.
func (s *Store) AllocUID(folderID int64) (uint32, error) {
	tx, err := s.idxdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := s.ensureIndexFolder(tx, folderID); err != nil {
		return 0, err
	}
	uid, err := allocateUID(tx, folderID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return uint32(uid), nil
}

// UIDNext returns the UID that will be assigned to the next message added to the
// folder (the IMAP UIDNEXT).
func (s *Store) UIDNext(folderID int64) (uint32, error) {
	v, err := s.idxFolderField(folderID, "uidnext")
	return uint32(v), err
}

// UIDValidity returns the folder's IMAP UIDVALIDITY.
func (s *Store) UIDValidity(folderID int64) (uint32, error) {
	v, err := s.idxFolderField(folderID, "uidvalidity")
	return uint32(v), err
}

// idxFolderField ensures the index folder row exists, then reads one of its
// integer columns. column is an internal constant, never caller input, so
// interpolating it into the SQL is safe.
func (s *Store) idxFolderField(folderID int64, column string) (int64, error) {
	tx, err := s.idxdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := s.ensureIndexFolder(tx, folderID); err != nil {
		return 0, err
	}
	var v int64
	if err := tx.QueryRow(
		fmt.Sprintf(`SELECT %s FROM folders WHERE folder_id=?`, column), folderID).Scan(&v); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return v, nil
}
