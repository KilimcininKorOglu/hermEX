package objectstore

import (
	"database/sql"
	"errors"
)

// FoundMessage is a message a search folder holds, with the folder it lives in.
// A search folder holds no messages of its own, so a client reaches a result by
// the folder and UID of the message itself.
type FoundMessage struct {
	Folder int64
	MessageInfo
}

// SearchFolderMessages returns the mail a search folder holds, in the order
// SearchFolderMessageIDs gives. A result that has no IMAP index entry (an item
// that is not mail) is left out, since every caller addresses mail by folder and
// UID.
func (s *Store) SearchFolderMessages(fid int64) ([]FoundMessage, error) {
	ids, err := s.SearchFolderMessageIDs(fid)
	if err != nil {
		return nil, err
	}
	out := make([]FoundMessage, 0, len(ids))
	for _, id := range ids {
		m, ok, err := s.searchResultRow(id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// searchResultRow reads one message's index row by its object-store id.
func (s *Store) searchResultRow(id int64) (FoundMessage, bool, error) {
	row := s.idxdb.QueryRow(`SELECT folder_id, `+messageInfoCols+` FROM messages WHERE message_id=?`, id)
	var folder int64
	info, err := scanMessageInfo(prefixScanner{row: row, first: &folder})
	if errors.Is(err, sql.ErrNoRows) {
		return FoundMessage{}, false, nil
	}
	if err != nil {
		return FoundMessage{}, false, err
	}
	return FoundMessage{Folder: folder, MessageInfo: info}, true, nil
}

// prefixScanner scans one leading column into first and the rest as a
// MessageInfo row.
type prefixScanner struct {
	row   *sql.Row
	first *int64
}

// Scan scans the row with the leading column prepended.
func (p prefixScanner) Scan(dest ...any) error {
	return p.row.Scan(append([]any{p.first}, dest...)...)
}

// SearchFolderCounts returns how many messages a search folder holds and how
// many of them are unread, the counts a folder listing reports for it.
func (s *Store) SearchFolderCounts(fid int64) (total, unread int, err error) {
	found, err := s.SearchFolderMessages(fid)
	if err != nil {
		return 0, 0, err
	}
	for _, m := range found {
		if m.Flags&FlagSeen == 0 {
			unread++
		}
	}
	return len(found), unread, nil
}
