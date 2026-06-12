package store

// Message flag bits persisted in the messages.flags column. These are the
// canonical storage encoding of the IMAP system flags; the storage format owns
// this mapping so other consumers (e.g. webmail) can read seen/deleted state
// without depending on the IMAP layer. The IMAP \Recent flag is per-session
// state, not message state, and is never persisted here.
const (
	FlagSeen     int64 = 1 << 0
	FlagAnswered int64 = 1 << 1
	FlagFlagged  int64 = 1 << 2
	FlagDeleted  int64 = 1 << 3
	FlagDraft    int64 = 1 << 4
)

// SetMessageFlags replaces a message's stored flag bits, identified by its
// folder and IMAP UID. It reports ErrNotFound when no such message exists.
func (s *Store) SetMessageFlags(folderID int64, uid uint32, flags int64) error {
	res, err := s.db.Exec(
		`UPDATE messages SET flags = ? WHERE folder_id = ? AND imap_uid = ?`,
		flags, folderID, int64(uid))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
