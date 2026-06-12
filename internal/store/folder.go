package store

import "time"

// CreateFolder creates a folder under parent (nil for a root folder) and
// returns its id. A fresh UIDVALIDITY is assigned and the UID sequence starts
// at 1.
func (s *Store) CreateFolder(parent *int64, displayName string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO folders (parent_id, display_name, uid_validity, next_uid) VALUES (?, ?, ?, 1)`,
		parent, displayName, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AllocUID atomically reserves and returns the next IMAP UID for a folder. UIDs
// are monotonic and never reused, even across restarts.
func (s *Store) AllocUID(folderID int64) (uint32, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var uid int64
	if err := tx.QueryRow(`SELECT next_uid FROM folders WHERE folder_id = ?`, folderID).Scan(&uid); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE folders SET next_uid = next_uid + 1 WHERE folder_id = ?`, folderID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return uint32(uid), nil
}

// UIDValidity returns the folder's IMAP UIDVALIDITY.
func (s *Store) UIDValidity(folderID int64) (uint32, error) {
	var v int64
	err := s.db.QueryRow(`SELECT uid_validity FROM folders WHERE folder_id = ?`, folderID).Scan(&v)
	return uint32(v), err
}
