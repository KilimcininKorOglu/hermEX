package store

import (
	"database/sql"
	"errors"
	"time"
)

// FolderInfo is the per-folder metadata needed to enumerate a mailbox's folder
// tree (e.g. for IMAP LIST). ParentID is nil for a root folder.
type FolderInfo struct {
	ID          int64
	ParentID    *int64
	DisplayName string
}

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

// FolderByName looks up a folder by its parent (nil for root) and display
// name, reporting ok=false when no such folder exists.
func (s *Store) FolderByName(parent *int64, name string) (id int64, ok bool, err error) {
	if parent == nil {
		err = s.db.QueryRow(
			`SELECT folder_id FROM folders WHERE parent_id IS NULL AND display_name = ?`, name).Scan(&id)
	} else {
		err = s.db.QueryRow(
			`SELECT folder_id FROM folders WHERE parent_id = ? AND display_name = ?`, *parent, name).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// ListFolders returns every folder in the mailbox, ordered by id.
func (s *Store) ListFolders() ([]FolderInfo, error) {
	rows, err := s.db.Query(`SELECT folder_id, parent_id, display_name FROM folders ORDER BY folder_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FolderInfo
	for rows.Next() {
		var f FolderInfo
		var parent sql.NullInt64
		if err := rows.Scan(&f.ID, &parent, &f.DisplayName); err != nil {
			return nil, err
		}
		if parent.Valid {
			p := parent.Int64
			f.ParentID = &p
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
