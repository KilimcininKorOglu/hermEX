package objectstore

import (
	"database/sql"
	"fmt"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// IMAP system-flag bits, the canonical storage encoding of the IMAP system
// flags. The index splits them into boolean columns; other consumers read this
// mask. The IMAP \Recent flag is per-session state, tracked by the recent
// column rather than this mask.
const (
	FlagSeen     int64 = 1 << 0
	FlagAnswered int64 = 1 << 1
	FlagFlagged  int64 = 1 << 2
	FlagDeleted  int64 = 1 << 3
	FlagDraft    int64 = 1 << 4
)

// indexMessage adds a message to the IMAP/POP3 index: it ensures the index
// folder row exists, allocates the next monotonic UID, and writes the
// denormalized index row (flags split into columns, envelope projections for
// listing and search) plus the id-to-mid mapping. It runs in its own
// transaction because the index is a separate database from the object store;
// the object-store row is committed first, so a crash between the two leaves an
// orphan that a folder reindex repairs. wireSize is the RFC822 byte size the
// message serves as (IMAP RFC822.SIZE); received is its INTERNALDATE. Returns
// the allocated UID.
func (s *Store) indexMessage(folderID, messageID int64, mid string, msg *oxcmail.Message, wireSize int64, received time.Time, flags int64) (int64, error) {
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
	var idx int64
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(idx), 0) + 1 FROM messages WHERE folder_id=?`, folderID).Scan(&idx); err != nil {
		return 0, err
	}

	// A new message takes a fresh modseq from the folder's CONDSTORE counter.
	modseq, err := nextModSeq(tx, folderID)
	if err != nil {
		return 0, err
	}
	bit := func(f int64) int {
		if flags&f != 0 {
			return 1
		}
		return 0
	}
	if _, err := tx.Exec(
		`INSERT INTO messages
		   (message_id, folder_id, mid_string, idx, mod_time, uid,
		    unsent, recent, read, flagged, replied, forwarded, deleted,
		    subject, sender, rcpt, size, received, modseq)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?)`,
		messageID, folderID, mid, idx, time.Now().Unix(), uid,
		bit(FlagDraft), bit(FlagSeen), bit(FlagFlagged), bit(FlagAnswered), bit(FlagDeleted),
		projectSubject(msg.Props), projectSender(msg.Props), projectRcpt(msg.Recipients),
		wireSize, received.Unix(), modseq); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(
		`INSERT INTO mapping (message_id, mid_string, flag_string) VALUES (?, ?, NULL)`,
		messageID, mid); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return uid, nil
}

// ensureIndexFolder creates the index folder row for folderID if it does not
// yet exist, mirroring the object-store folder's parent and display name and
// starting UID allocation at 1.
func (s *Store) ensureIndexFolder(tx *sql.Tx, folderID int64) error {
	var exists int
	err := tx.QueryRow(`SELECT 1 FROM folders WHERE folder_id=?`, folderID).Scan(&exists)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	parent, name, commitMax, err := s.folderIndexInfo(folderID)
	if err != nil {
		return err
	}
	// UIDVALIDITY is assigned once at folder-index creation (its initial epoch);
	// only a reindex that cannot preserve the mid-to-UID mapping bumps it.
	_, err = tx.Exec(
		`INSERT INTO folders (folder_id, parent_fid, commit_max, name, uidnext, uidvalidity) VALUES (?, ?, ?, ?, 1, ?)`,
		folderID, parent, commitMax, name, time.Now().Unix())
	return err
}

// folderIndexInfo reads the object-store folder's parent, display name, and
// change number to project into the index folder row.
func (s *Store) folderIndexInfo(folderID int64) (parent int64, name string, commitMax int64, err error) {
	var parentNull sql.NullInt64
	if err = s.objdb.QueryRow(
		`SELECT parent_id, change_number FROM folders WHERE folder_id=?`, folderID).Scan(&parentNull, &commitMax); err != nil {
		return 0, "", 0, err
	}
	parent = parentNull.Int64
	props, err := s.GetFolderProperties(folderID, mapi.PrDisplayName)
	if err != nil {
		return 0, "", 0, err
	}
	if dn, ok := stringProp(props, mapi.PrDisplayName); ok && dn != "" {
		name = dn
	} else {
		name = fmt.Sprintf("folder-%d", folderID)
	}
	return parent, name, commitMax, nil
}

// allocateUID returns the folder's next IMAP UID and advances the counter, so
// UIDs are monotonic and never reused.
func allocateUID(tx *sql.Tx, folderID int64) (int64, error) {
	var uid int64
	if err := tx.QueryRow(`SELECT uidnext FROM folders WHERE folder_id=?`, folderID).Scan(&uid); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE folders SET uidnext=uidnext+1 WHERE folder_id=?`, folderID); err != nil {
		return 0, err
	}
	return uid, nil
}

// projectSubject is the index's searchable and sortable subject.
func projectSubject(props mapi.PropertyValues) string {
	if s, ok := stringProp(props, mapi.PrSubject); ok {
		return s
	}
	if s, ok := stringProp(props, mapi.PrNormalizedSubject); ok {
		return s
	}
	return ""
}

// projectSender is the index's searchable and sortable originator, formatted
// from the sent-representing identity (falling back to the sender identity).
func projectSender(props mapi.PropertyValues) string {
	if a := formatIdentity(props, mapi.PrSentRepresentingName, mapi.PrSentRepresentingSmtpAddress); a != "" {
		return a
	}
	return formatIdentity(props, mapi.PrSenderName, mapi.PrSenderSmtpAddress)
}

// projectRcpt is the index's searchable primary recipient: the first To
// recipient, else the first recipient of any kind.
func projectRcpt(recips []mapi.PropertyValues) string {
	var fallback string
	for _, r := range recips {
		addr := formatIdentity(r, mapi.PrDisplayName, mapi.PrSmtpAddress)
		if addr == "" {
			continue
		}
		if v, ok := r.Get(mapi.PrRecipientType); ok {
			if t, ok := v.(int32); ok && t == int32(mapi.RecipTo) {
				return addr
			}
		}
		if fallback == "" {
			fallback = addr
		}
	}
	return fallback
}

// formatIdentity renders "Name <addr>", "addr", "Name", or "" from a
// name/address property pair.
func formatIdentity(props mapi.PropertyValues, nameTag, addrTag mapi.PropTag) string {
	name, _ := stringProp(props, nameTag)
	addr, _ := stringProp(props, addrTag)
	switch {
	case name != "" && addr != "":
		return name + " <" + addr + ">"
	case addr != "":
		return addr
	default:
		return name
	}
}

// stringProp reads a string-typed property, reporting whether it was present
// and a string.
func stringProp(props mapi.PropertyValues, tag mapi.PropTag) (string, bool) {
	if v, ok := props.Get(tag); ok {
		if s, ok := v.(string); ok {
			return s, true
		}
	}
	return "", false
}
