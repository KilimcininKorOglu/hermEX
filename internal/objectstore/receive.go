package objectstore

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"hermex/internal/mapi"
)

// ReceiveFolderEntry is one row of the receive-folder table: the message class
// it routes, the folder that class is delivered to, and when the mapping last
// changed (an NT FILETIME).
type ReceiveFolderEntry struct {
	Class        string
	FolderID     int64
	ModifiedTime uint64
}

// GetReceiveFolder resolves a message class to the folder its mail is delivered
// to ([MS-OXCSTOR] 2.2.1.2). It walks the dotted class from the longest prefix
// to the shortest (so "IPM.Note.Foo" tries "IPM.Note.Foo", then "IPM.Note",
// then "IPM"), then the empty default class, and finally falls back to the Inbox
// — so a private store always resolves. It returns the folder id and the
// explicit class that matched (the empty string for the default or the Inbox
// fallback).
func (s *Store) GetReceiveFolder(messageClass string) (folderID int64, explicitClass string, err error) {
	for cls := messageClass; cls != ""; {
		fid, ok, e := s.receiveFolderByClass(cls)
		if e != nil {
			return 0, "", e
		}
		if ok {
			return fid, cls, nil
		}
		if dot := strings.LastIndexByte(cls, '.'); dot >= 0 {
			cls = cls[:dot]
		} else {
			break
		}
	}
	// The empty default class is the catch-all.
	if fid, ok, e := s.receiveFolderByClass(""); e != nil {
		return 0, "", e
	} else if ok {
		return fid, "", nil
	}
	// No mapping at all: deliver to the Inbox.
	return int64(mapi.PrivateFIDInbox), "", nil
}

// receiveFolderByClass looks up the exact class row (case-insensitive, per the
// table's NOCASE collation), reporting whether one exists.
func (s *Store) receiveFolderByClass(class string) (int64, bool, error) {
	var fid int64
	err := s.objdb.QueryRow(`SELECT folder_id FROM receive_table WHERE class=?`, class).Scan(&fid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return fid, true, nil
}

// SetReceiveFolder sets the receive folder for a message class, or removes the
// mapping when folderID is 0 ([MS-OXCSTOR] 2.2.1.3). A non-zero folder must
// exist. The class column is unique, so an existing mapping is upserted with a
// fresh modification time. Class-level policy (the un-settable IPM/REPORT.IPM
// classes, the empty-class guard) is enforced by the ROP layer.
func (s *Store) SetReceiveFolder(messageClass string, folderID int64) error {
	if folderID == 0 {
		_, err := s.objdb.Exec(`DELETE FROM receive_table WHERE class=?`, messageClass)
		return err
	}
	exists, err := s.FolderExists(folderID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	now := int64(mapi.UnixToNTTime(time.Now()))
	_, err = s.objdb.Exec(
		`INSERT INTO receive_table (class, folder_id, modified_time) VALUES (?, ?, ?)
		 ON CONFLICT(class) DO UPDATE SET folder_id=excluded.folder_id, modified_time=excluded.modified_time`,
		messageClass, folderID, now)
	return err
}

// ReceiveFolderTable returns every receive-folder mapping ([MS-OXCSTOR] 2.2.1.4),
// ordered by class for a stable enumeration.
func (s *Store) ReceiveFolderTable() ([]ReceiveFolderEntry, error) {
	rows, err := s.objdb.Query(`SELECT class, folder_id, modified_time FROM receive_table ORDER BY class`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReceiveFolderEntry
	for rows.Next() {
		var e ReceiveFolderEntry
		var mt int64
		if err := rows.Scan(&e.Class, &e.FolderID, &mt); err != nil {
			return nil, err
		}
		e.ModifiedTime = uint64(mt)
		out = append(out, e)
	}
	return out, rows.Err()
}
