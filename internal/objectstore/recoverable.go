package objectstore

import (
	"database/sql"
	"errors"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// SoftDeletedMessage is one entry in a folder's Recoverable Items dumpster: a
// message that was deleted but is still recoverable. It carries the projections a
// recover UI lists, addressed by MessageID (a soft-deleted item has no IMAP UID,
// since it is dropped from the index).
type SoftDeletedMessage struct {
	MessageID int64
	Subject   string
	Sender    string
	Date      time.Time // delivery time
	DeletedOn time.Time // when soft-deleted (PR_DELETED_ON), zero if unstamped
	Size      int64
}

// SoftDeleteMessage moves a message into the Recoverable Items dumpster. Faithful
// to the reference, the dumpster is not a separate folder: the object row stays in
// its folder, flagged is_deleted=1 with a fresh change number, and a PR_DELETED_ON
// timestamp records when. The IMAP/POP3 index row is dropped so the message
// vanishes from every live view (and is reported deleted to ICS clients, which read
// only live rows). The object row, its properties, and the cached eml all survive,
// so the message is recoverable until a true purge. It reports ErrNotFound when no
// such message exists in the folder.
func (s *Store) SoftDeleteMessage(folderID int64, uid uint32) error {
	var messageID int64
	err := s.idxdb.QueryRow(
		`SELECT message_id FROM messages WHERE folder_id=? AND uid=?`,
		folderID, int64(uid)).Scan(&messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return s.softDeleteRow(messageID)
}

// SoftDeleteObject soft-deletes a message addressed by its object id (the form the
// ROP layer uses, where a message is named by EID rather than IMAP uid), routing it
// to the Recoverable Items dumpster. It is idempotent: a message already in the
// dumpster is left as is. It reports ErrNotFound when no such object exists.
func (s *Store) SoftDeleteObject(messageID int64) error {
	var isDeleted int
	err := s.objdb.QueryRow(`SELECT is_deleted FROM messages WHERE message_id=?`, messageID).Scan(&isDeleted)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if isDeleted == 1 {
		return nil
	}
	return s.softDeleteRow(messageID)
}

// softDeleteRow flags an already-resolved message into the dumpster: is_deleted=1
// with a fresh change number, a PR_DELETED_ON stamp, and its IMAP index row dropped
// so it leaves every live view. The object row, its properties, and the cached eml
// survive for recovery. Shared by the uid-addressed SoftDeleteMessage and the
// id-addressed SoftDeleteObject.
func (s *Store) softDeleteRow(messageID int64) error {
	// Flip the flag and bump the change number atomically; allocateCN is a
	// read-then-write counter, so it must share the update's transaction.
	tx, err := s.objdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cn, err := allocateCN(tx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE messages SET is_deleted=1, change_number=? WHERE message_id=?`,
		int64(cn), messageID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// Stamp the soft-delete time so retention can age it out. Best-effort and in its
	// own transaction: retention only purges items that carry a PR_DELETED_ON older
	// than the cutoff, so an interruption before this stamp lands keeps the item
	// (never loses a recoverable message).
	if err := s.SetMessageProperties(messageID, mapi.PropertyValues{
		{Tag: mapi.PrDeletedOn, Value: mapi.UnixToNTTime(time.Now())},
	}); err != nil {
		return err
	}

	// Drop the index row last; the object row and eml stay for recovery.
	if _, err := s.idxdb.Exec(`DELETE FROM messages WHERE message_id=?`, messageID); err != nil {
		return err
	}
	if _, err := s.idxdb.Exec(`DELETE FROM mapping WHERE message_id=?`, messageID); err != nil {
		return err
	}
	return nil
}

// ListSoftDeleted returns the Recoverable Items dumpster for a folder: the
// messages soft-deleted from it, newest soft-deletion first. It is the
// SHOW_SOFT_DELETES view a recover UI lists.
func (s *Store) ListSoftDeleted(folderID int64) ([]SoftDeletedMessage, error) {
	rows, err := s.objdb.Query(
		`SELECT message_id, message_size FROM messages WHERE parent_fid=? AND is_deleted=1`, folderID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	sizes := map[int64]int64{}
	for rows.Next() {
		var id, size int64
		if err := rows.Scan(&id, &size); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		sizes[id] = size
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]SoftDeletedMessage, 0, len(ids))
	for _, id := range ids {
		msg, err := s.OpenMessage(id)
		if err != nil {
			return nil, err
		}
		out = append(out, SoftDeletedMessage{
			MessageID: id,
			Subject:   projectSubject(msg.Props),
			Sender:    projectSender(msg.Props),
			Date:      deliveryTime(msg.Props),
			DeletedOn: sysTimeProp(msg.Props, mapi.PrDeletedOn),
			Size:      sizes[id],
		})
	}
	// Newest soft-deletion first.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].DeletedOn.After(out[i].DeletedOn) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

// ListSoftDeletedInfo returns a folder's soft-deleted messages as index-shaped
// MessageInfo rows. It backs the MAPI/ROP SHOW_SOFT_DELETES contents table: the
// rows no longer live in the IMAP index, so they are read straight from the object
// store, and only the message ID is needed (the table materializes each row's
// properties from the object store by that ID).
func (s *Store) ListSoftDeletedInfo(folderID int64) ([]MessageInfo, error) {
	rows, err := s.objdb.Query(
		`SELECT message_id, message_size FROM messages WHERE parent_fid=? AND is_deleted=1 ORDER BY message_id`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageInfo
	for rows.Next() {
		var m MessageInfo
		if err := rows.Scan(&m.ID, &m.Size); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RecoverMessage restores a soft-deleted message from the dumpster back into its
// folder: it clears is_deleted with a fresh change number (so ICS clients re-add
// it) and rebuilds the IMAP/POP3 index row from the surviving object. The recovered
// message receives a new UID. It reports ErrNotFound when the message is not a
// soft-deleted item of the folder.
func (s *Store) RecoverMessage(folderID, messageID int64) (MessageInfo, error) {
	var parent int64
	var isDeleted, readSt int
	err := s.objdb.QueryRow(
		`SELECT parent_fid, is_deleted, read_state FROM messages WHERE message_id=?`,
		messageID).Scan(&parent, &isDeleted, &readSt)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageInfo{}, ErrNotFound
	}
	if err != nil {
		return MessageInfo{}, err
	}
	if parent != folderID || isDeleted != 1 {
		return MessageInfo{}, ErrNotFound
	}

	tx, err := s.objdb.Begin()
	if err != nil {
		return MessageInfo{}, err
	}
	defer tx.Rollback()
	cn, err := allocateCN(tx)
	if err != nil {
		return MessageInfo{}, err
	}
	if _, err := tx.Exec(
		`UPDATE messages SET is_deleted=0, change_number=? WHERE message_id=?`,
		int64(cn), messageID); err != nil {
		return MessageInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return MessageInfo{}, err
	}

	// Rebuild the served eml and index row from the surviving object, mirroring a
	// single-message reindex.
	msg, err := s.OpenMessage(messageID)
	if err != nil {
		return MessageInfo{}, err
	}
	mid := midString(uint64(messageID))
	eml, err := oxcmail.Export(msg, oxcmail.Options{Resolver: s.GetNamedPropIDs})
	if err != nil {
		return MessageInfo{}, err
	}
	if err := s.writeEML(mid, eml); err != nil {
		return MessageInfo{}, err
	}
	var flags int64
	if readSt != 0 {
		flags |= FlagSeen
	}
	received := deliveryTime(msg.Props)
	uid, err := s.indexMessage(folderID, messageID, mid, msg, int64(len(eml)), received, flags)
	if err != nil {
		return MessageInfo{}, err
	}
	return MessageInfo{
		ID:           messageID,
		UID:          uint32(uid),
		InternalDate: received.UTC(),
		Size:         int64(len(eml)),
		Flags:        flags,
		Subject:      projectSubject(msg.Props),
		Sender:       projectSender(msg.Props),
	}, nil
}

// PurgeSoftDeleted permanently removes a single soft-deleted message from the
// dumpster (the explicit "delete from Recoverable Items"). It refuses a message
// that is not soft-deleted, so a live message can never be purged through this
// path. The removal itself is the object-store hard delete (row + cascade + eml).
func (s *Store) PurgeSoftDeleted(messageID int64) error {
	var isDeleted int
	err := s.objdb.QueryRow(`SELECT is_deleted FROM messages WHERE message_id=?`, messageID).Scan(&isDeleted)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if isDeleted != 1 {
		return ErrNotFound
	}
	return s.DeleteObject(messageID)
}

// PurgeSoftDeletedInFolder is the folder-scoped purge: it permanently removes a
// soft-deleted message only when it is in the named folder's dumpster, so a caller
// authorized on one folder cannot purge another folder's item. It reports
// ErrNotFound otherwise.
func (s *Store) PurgeSoftDeletedInFolder(folderID, messageID int64) error {
	var parent int64
	var isDeleted int
	err := s.objdb.QueryRow(
		`SELECT parent_fid, is_deleted FROM messages WHERE message_id=?`,
		messageID).Scan(&parent, &isDeleted)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if parent != folderID || isDeleted != 1 {
		return ErrNotFound
	}
	return s.DeleteObject(messageID)
}

// PurgeSoftDeletedOlderThan is the retention sweep: it permanently removes every
// soft-deleted message whose PR_DELETED_ON is older than cutoff, mailbox-wide, and
// reports how many it purged. An item without a PR_DELETED_ON stamp is kept (it is
// never aged out blindly).
func (s *Store) PurgeSoftDeletedOlderThan(cutoff time.Time) (int, error) {
	rows, err := s.objdb.Query(`SELECT message_id FROM messages WHERE is_deleted=1`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	purged := 0
	for _, id := range ids {
		msg, err := s.OpenMessage(id)
		if err != nil {
			return purged, err
		}
		deletedOn := sysTimeProp(msg.Props, mapi.PrDeletedOn)
		if deletedOn.IsZero() || !deletedOn.Before(cutoff) {
			continue
		}
		if err := s.DeleteObject(id); err != nil {
			return purged, err
		}
		purged++
	}
	return purged, nil
}

// sysTimeProp reads a PtSysTime message property as a time.Time, returning the zero
// time when the property is absent or not a FILETIME.
func sysTimeProp(props mapi.PropertyValues, tag mapi.PropTag) time.Time {
	v, ok := props.Get(tag)
	if !ok {
		return time.Time{}
	}
	nt, ok := v.(uint64)
	if !ok {
		return time.Time{}
	}
	return mapi.NTTimeToUnix(nt)
}
