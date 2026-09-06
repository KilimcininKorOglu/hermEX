package objectstore

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"hermex/internal/ics"
	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// Import flags for RopSynchronizationImportMessageChange ([MS-OXCFXICS]
// 2.2.3.2.4.2): ASSOCIATED marks the imported message FAI; FAILONCONFLICT asks a
// detected predecessor-list conflict to fail rather than create a conflict-resolve
// message. v1 honors ASSOCIATED and accepts FAILONCONFLICT without acting on it
// (it derives no stored predecessor list, so it never detects a conflict).
const (
	ImportFlagAssociated     uint8 = 0x10
	ImportFlagFailOnConflict uint8 = 0x40
)

// UploadMessage is one message being imported through the ICS upload path: the
// identity resolved from a RopSynchronizationImportMessageChange header (the
// destination folder, the home-replica message id, the associated flag, and
// whether that id already exists) plus the message bag a MessageCollector fills
// from the FastTransfer content stream. The wire splits the operation across two
// ROPs, the import-change ROP returns a message handle, then a FastTransfer
// destination fills it, so identity and content are kept apart here: this holds
// the identity, NewMessageCollector binds a collector to it, and Commit writes the
// assembled message under the resolved id.
type UploadMessage struct {
	store      *Store
	folderID   int64
	mid        uint64
	associated bool
	isNew      bool
	msg        *oxcmail.Message
}

// ImportMessageChange resolves a message-change header into an UploadMessage
// ([MS-OXCFXICS] 3.3.5.10). The header carries the four identity properties the
// wire sends in fixed order, PR_SOURCE_KEY, PR_LAST_MODIFICATION_TIME,
// PR_CHANGE_KEY, PR_PREDECESSOR_CHANGE_LIST, of which the 22-byte source key
// names the message: a home-replica source key yields the message id, and the
// store decides new-vs-existing from it. The change key and predecessor list are
// accepted but not stored, the store derives both from the change number on
// download, so v1 does no predecessor-list conflict detection (last writer wins,
// a documented limitation). A foreign-replica source key (a cross-store import) is
// rejected in v1.
func (s *Store) ImportMessageChange(folderID int64, importFlags uint8, header mapi.PropertyValues) (*UploadMessage, error) {
	if importFlags&^(ImportFlagAssociated|ImportFlagFailOnConflict) != 0 {
		return nil, fmt.Errorf("objectstore: unsupported import flags %#x", importFlags)
	}
	sk, ok := propBytes(header, mapi.PrSourceKey)
	if !ok {
		return nil, fmt.Errorf("objectstore: import message change missing PR_SOURCE_KEY")
	}
	for _, tag := range []mapi.PropTag{mapi.PrLastModificationTime, mapi.PrChangeKey, mapi.PrPredecessorChangeList} {
		if _, ok := header.Get(tag); !ok {
			return nil, fmt.Errorf("objectstore: import message change missing %s", tag)
		}
	}
	home, err := s.replicaGUID()
	if err != nil {
		return nil, err
	}
	mid, foreign, err := parseSourceKeyMID(sk, home)
	if err != nil {
		return nil, err
	}
	if foreign {
		return nil, fmt.Errorf("objectstore: cross-store message import is not supported in v1")
	}
	var exists int
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	err = s.objdb.QueryRow(`SELECT 1 FROM messages WHERE message_id=?`, int64(mid)).Scan(&exists)
	isNew := err == sql.ErrNoRows
	if err != nil && !isNew {
		return nil, err
	}
	return &UploadMessage{
		store:      s,
		folderID:   folderID,
		mid:        mid,
		associated: importFlags&ImportFlagAssociated != 0,
		isNew:      isNew,
		msg:        &oxcmail.Message{},
	}, nil
}

// parseSourceKeyMID reads a 22-byte PR_SOURCE_KEY the way the store builds it (the
// replica GUID flat-form followed by the 6-byte global counter of the id). A key
// whose replica is the home store yields the message id; any other replica is
// reported foreign for the caller to reject.
func parseSourceKeyMID(sk []byte, home mapi.GUID) (mid uint64, foreign bool, err error) {
	if len(sk) != 22 {
		return 0, false, fmt.Errorf("objectstore: source key must be 22 bytes, got %d", len(sk))
	}
	hf := home.Flat()
	if !bytes.Equal(sk[:16], hf[:]) {
		return 0, true, nil
	}
	var gc mapi.GlobCnt
	copy(gc[:], sk[16:])
	return mapi.GCToValue(gc), false, nil
}

// propBytes reads a binary property value from a bag.
func propBytes(pv mapi.PropertyValues, tag mapi.PropTag) ([]byte, bool) {
	v, ok := pv.Get(tag)
	if !ok {
		return nil, false
	}
	b, ok := v.([]byte)
	return b, ok
}

// MessageCollector assembles the body of one uploaded message from a FastTransfer
// content stream, the MESSAGECONTENT root form a client sends after an
// import-change ROP. Top-level properties land on the message; STARTRECIP/
// ENDTORECIP frame a recipient bag and NEWATTACH/ENDATTACH an attachment bag;
// MetaTagFXDelProp(PR_MESSAGE_RECIPIENTS|PR_MESSAGE_ATTACHMENTS) resets that
// collection before the incoming members are applied. Named properties carried
// inline are remapped to the store's own ids. PutBuffer accepts arbitrary
// transport chunks. Embedded-message markers (a documented v1 limitation), the
// bulk-copy message-list markers, and any ICS state meta-tag are rejected rather
// than mishandled.
type MessageCollector struct {
	um     *UploadMessage
	parser ics.Parser
	frame  *collectorFrame // the open recipient/attachment, or nil at the message root
}

// collectorFrame is one open child object on the (single-level) marker stack: the
// START marker that opened it and the property bag accumulating until its END.
type collectorFrame struct {
	marker uint32
	bag    mapi.PropertyValues
}

// NewMessageCollector binds a collector to a resolved UploadMessage so the
// FastTransfer content stream fills it.
func NewMessageCollector(um *UploadMessage) *MessageCollector {
	return &MessageCollector{um: um}
}

// PutBuffer feeds one transport chunk of the content stream, applying every
// complete element it now holds. A chunk may split an element at any byte
// boundary; the parser retains the partial element until the next call.
func (c *MessageCollector) PutBuffer(chunk []byte) error {
	c.parser.Feed(chunk)
	for {
		it, ok, err := c.parser.Next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if it.IsMarker {
			if err := c.recordMarker(it.Marker); err != nil {
				return err
			}
			continue
		}
		if err := c.recordProp(*it.Prop); err != nil {
			return err
		}
	}
}

// recordMarker advances the single-level marker stack. The only valid nesting in a
// message-content stream is one recipient or one attachment at a time; embedded
// messages and the bulk-copy message-list markers are rejected.
func (c *MessageCollector) recordMarker(m uint32) error {
	switch m {
	case ics.MarkerStartRecip:
		return c.openFrame(ics.MarkerStartRecip, "STARTRECIP")
	case ics.MarkerEndToRecip:
		bag, err := c.closeFrame(ics.MarkerStartRecip, "ENDTORECIP", "STARTRECIP")
		if err != nil {
			return err
		}
		c.um.msg.Recipients = append(c.um.msg.Recipients, bag)
	case ics.MarkerNewAttach:
		return c.openFrame(ics.MarkerNewAttach, "NEWATTACH")
	case ics.MarkerEndAttach:
		bag, err := c.closeFrame(ics.MarkerNewAttach, "ENDATTACH", "NEWATTACH")
		if err != nil {
			return err
		}
		c.um.msg.Attachments = append(c.um.msg.Attachments, oxcmail.Attachment{Props: bag})
	case ics.MarkerStartEmbed, ics.MarkerEndEmbed:
		return fmt.Errorf("objectstore: embedded-message upload is not supported in v1")
	case ics.MarkerStartMessage, ics.MarkerEndMessage:
		return fmt.Errorf("objectstore: message-list (bulk copy) upload is not supported in v1")
	default:
		return fmt.Errorf("objectstore: unexpected upload marker %#x", m)
	}
	return nil
}

// openFrame starts a child object. The stream is flat in v1, so a marker
// arriving while another frame is open is a nesting the collector cannot model.
func (c *MessageCollector) openFrame(marker uint32, name string) error {
	if c.frame != nil {
		return fmt.Errorf("objectstore: %s nested inside marker %#x", name, c.frame.marker)
	}
	c.frame = &collectorFrame{marker: marker}
	return nil
}

// closeFrame ends the open child object and hands back its property bag. The
// closing marker must match the one that opened the frame, or the stream is
// misframed and the properties would land on the wrong collection.
func (c *MessageCollector) closeFrame(marker uint32, closing, opening string) (mapi.PropertyValues, error) {
	if c.frame == nil || c.frame.marker != marker {
		return nil, fmt.Errorf("objectstore: %s without an open %s", closing, opening)
	}
	bag := c.frame.bag
	c.frame = nil
	return bag, nil
}

// recordProp routes one property to the open object. State meta-tags never travel
// in the content stream (they arrive via the upload-state-stream ROPs) and are
// rejected; MetaTagFXDelProp resets a child collection; a named property is
// remapped to a store-local id; everything else is set on the current bag.
func (c *MessageCollector) recordProp(p ics.StreamProp) error {
	if ics.IsStateMetaTag(uint32(p.Tag)) {
		return fmt.Errorf("objectstore: ICS state meta-tag %#x in content stream", uint32(p.Tag))
	}
	if uint32(p.Tag) == ics.MetaTagFXDelProp {
		return c.delProp(p.Value)
	}
	tag, err := c.resolveTag(p)
	if err != nil {
		return err
	}
	if c.frame != nil {
		c.frame.bag.Set(tag, p.Value)
	} else {
		c.um.msg.Props.Set(tag, p.Value)
	}
	return nil
}

// delProp applies a MetaTagFXDelProp directive: its PT_LONG value names a
// collection on the current message to clear before the incoming members follow.
// v1 supports the recipient and attachment collections, only at the message root.
func (c *MessageCollector) delProp(v any) error {
	tag, ok := v.(int32)
	if !ok {
		return fmt.Errorf("objectstore: MetaTagFXDelProp value is %T, want PT_LONG", v)
	}
	if c.frame != nil {
		return fmt.Errorf("objectstore: MetaTagFXDelProp inside marker %#x", c.frame.marker)
	}
	// #nosec G115 -- the signed and unsigned views of the same 32 bits
	switch mapi.PropTag(uint32(tag)) {
	case mapi.PrMessageRecipients:
		c.um.msg.Recipients = nil
	case mapi.PrMessageAttachments:
		c.um.msg.Attachments = nil
	default:
		// #nosec G115 -- the signed and unsigned views of the same 32 bits
		return fmt.Errorf("objectstore: unsupported MetaTagFXDelProp target %#x", uint32(tag))
	}
	return nil
}

// resolveTag returns the store-local tag for a stream property: a tagged property
// is used as-is, while a named property's inline name is allocated (or matched) to
// a store-local id and recombined with the wire type.
func (c *MessageCollector) resolveTag(p ics.StreamProp) (mapi.PropTag, error) {
	if p.Name == nil {
		return p.Tag, nil
	}
	ids, err := c.um.store.GetNamedPropIDs(true, []mapi.PropertyName{*p.Name})
	if err != nil {
		return 0, err
	}
	if ids[0] == 0 {
		return 0, fmt.Errorf("objectstore: could not allocate id for uploaded named property")
	}
	return mapi.PropTag(uint32(ids[0])<<16 | uint32(p.Tag.Type())), nil
}

// Commit writes the assembled message under its resolved id in one transaction. A
// new id is inserted and the destination folder's allocation cursor is advanced
// past it so a later server-side allocation never reuses it; an existing id is
// replaced wholesale, the messages-row delete cascades to its old property bag,
// recipients, attachments, and time-index row, and re-inserted. Either way a
// fresh change number is allocated, which is what a later download reports as the
// modification ("updated") that re-importing a message represents. Content
// properties are offloaded to content files by the property layer. It returns the
// message id.
func (um *UploadMessage) Commit() (uint64, error) {
	tx, err := um.store.objdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	cn, err := allocateCN(tx)
	if err != nil {
		return 0, err
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	id := int64(um.mid)
	if err := um.clearTarget(tx, id); err != nil {
		return 0, err
	}
	if err := um.insertRow(tx, id, cn); err != nil {
		return 0, err
	}
	if err := um.insertSubObjects(tx, id); err != nil {
		return 0, err
	}
	if err := insertMsgTime(tx, um.folderID, id, um.msg.Props); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	// A replace reuses the message id, and the eml cache is keyed by that id
	// alone, so the pre-edit wire form would otherwise keep being served to every
	// reader that goes through the cache (IMAP FETCH, POP3 RETR, the webmail
	// render), making the client's edit look lost. Unconditional like the other
	// message mutators: for a new id there is no cache file and this is a no-op.
	um.store.refreshEML(id)
	op := "modify"
	if um.isNew {
		op = "create"
	}
	um.store.publishChange(op, cn, midString(um.mid))
	return um.mid, nil
}

// clearTarget prepares the id the upload writes to: a new id advances the
// folder's allocation cursor past it, so a later server-side allocation never
// reuses it, while a replace deletes the existing row and lets the foreign keys
// take its properties, recipients and attachments with it.
func (um *UploadMessage) clearTarget(tx *sql.Tx, id int64) error {
	if um.isNew {
		return advanceFolderEID(tx, um.folderID, um.mid)
	}
	_, err := tx.Exec(`DELETE FROM messages WHERE message_id=?`, id)
	return err
}

// insertRow writes the messages row and its property bag.
func (um *UploadMessage) insertRow(tx *sql.Tx, id int64, cn uint64) error {
	assoc := 0
	if um.associated {
		assoc = 1
	}
	if _, err := tx.Exec(
		`INSERT INTO messages
		   (message_id, parent_fid, is_associated, change_number, read_state, message_size, mid_string)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		id, um.folderID, assoc, int64(cn), readState(um.msg.Props), messageSize(um.msg), midString(um.mid)); err != nil {
		return err
	}
	return um.store.insertProps(tx, "message_properties", "message_id", id, um.msg.Props)
}

// insertSubObjects writes the message's recipients and attachments, each as its
// own row plus a property bag.
func (um *UploadMessage) insertSubObjects(tx *sql.Tx, id int64) error {
	for _, rcpt := range um.msg.Recipients {
		if err := um.store.insertChildProps(tx, "recipients", "recipients_properties", "recipient_id", id, rcpt); err != nil {
			return err
		}
	}
	for _, att := range um.msg.Attachments {
		if err := um.store.insertChildProps(tx, "attachments", "attachment_properties", "attachment_id", id, att.Props); err != nil {
			return err
		}
	}
	return nil
}

// insertChildProps inserts one child row owned by a message and writes its
// property bag under the row's generated id.
func (s *Store) insertChildProps(tx *sql.Tx, table, propTable, propKey string, messageID int64, props mapi.PropertyValues) error {
	// #nosec G202 -- table and propTable are package constants naming this schema's own tables, never client input
	res, err := tx.Exec(`INSERT INTO `+table+` (message_id) VALUES (?)`, messageID)
	if err != nil {
		return err
	}
	rowID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	return s.insertProps(tx, propTable, propKey, rowID, props)
}

// advanceFolderEID bumps a folder's allocation cursor past an id imported into its
// reserved range, so a later server-side allocation never reuses it. An id outside
// the folder's current range (a client id drawn from a separately reserved block)
// matches nothing and is left alone.
func advanceFolderEID(q sqlExec, folderID int64, mid uint64) error {
	_, err := q.Exec(
		`UPDATE folders SET cur_eid=?+1 WHERE folder_id=? AND ?>=cur_eid AND ?<=max_eid`,
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		int64(mid), folderID, int64(mid), int64(mid))
	return err
}

// ImportDeletes removes the messages a client reports gone from a folder
// ([MS-OXCFXICS] 3.3.5.10). Each source key is a 22-byte XID; a home-replica one
// names a message id, which is deleted when it is present in the folder.
// Foreign-replica keys (cross-store) and ids absent from the folder are skipped,
// so the operation is idempotent. v1 always hard-deletes, the store keeps no
// soft-delete state, so the soft/hard distinction the wire carries is a
// documented limitation. It returns the ids actually deleted.
func (s *Store) ImportDeletes(folderID int64, sourceKeys [][]byte) ([]uint64, error) {
	home, err := s.replicaGUID()
	if err != nil {
		return nil, err
	}
	var deleted []uint64
	for _, sk := range sourceKeys {
		mid, foreign, err := parseSourceKeyMID(sk, home)
		if err != nil {
			return nil, err
		}
		if foreign {
			continue
		}
		var present int
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		err = s.objdb.QueryRow(`SELECT 1 FROM messages WHERE message_id=? AND parent_fid=?`, int64(mid), folderID).Scan(&present)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		if err := s.DeleteObject(int64(mid)); err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		deleted = append(deleted, mid)
	}
	return deleted, nil
}

// MoveMessageImport relocates a message from srcFolderID into destFolderID, renaming
// its id to dstMID and assigning a fresh change number. It is the store side of
// RopSynchronizationImportMessageMove ([MS-OXCFXICS] 3.3.5.9), where the client has
// already chosen the destination id in its own replica. Every message child table
// (properties, recipients, attachments, change rows, time index) renames the id for
// free through ON UPDATE CASCADE; only the time index's parent column is repointed
// explicitly, because it keys on (folder_id, message_id) and does not follow the
// message's parent. A retried move that re-sends a destination id already committed
// replaces it. A source the store no longer holds in srcFolderID yields
// ErrObjectDeleted. It returns whether the moved message was associated (FAI).
func (s *Store) MoveMessageImport(srcFolderID, srcMID, destFolderID, dstMID int64) (bool, error) {
	var assoc int
	err := s.objdb.QueryRow(
		`SELECT is_associated FROM messages WHERE message_id=? AND parent_fid=? AND is_deleted=0`,
		srcMID, srcFolderID).Scan(&assoc)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrObjectDeleted
	}
	if err != nil {
		return false, err
	}

	tx, err := s.objdb.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	cn, err := allocateCN(tx)
	if err != nil {
		return false, err
	}
	if err := renameMovedMessage(tx, srcFolderID, srcMID, destFolderID, dstMID, cn); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if err := s.dropMovedSourceIndex(srcMID); err != nil {
		return assoc != 0, err
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	s.publishChange("create", cn, midString(uint64(dstMID)))
	return assoc != 0, nil
}

// renameMovedMessage rewrites the message row to its destination id and folder.
// A retried move that re-sends a committed destination id replaces it. The time
// index is repointed explicitly because it keys on (folder_id, message_id) and
// does not follow the message's parent; every other child table renames for free
// through ON UPDATE CASCADE.
func renameMovedMessage(tx *sql.Tx, srcFolderID, srcMID, destFolderID, dstMID int64, cn uint64) error {
	if dstMID != srcMID {
		if _, err := tx.Exec(`DELETE FROM messages WHERE message_id=?`, dstMID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(
		`UPDATE messages SET message_id=?, parent_fid=?, change_number=?, mid_string=? WHERE message_id=?`,
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		dstMID, destFolderID, int64(cn), midString(uint64(dstMID)), srcMID); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE msgtime_index SET folder_id=? WHERE message_id=? AND folder_id=?`,
		destFolderID, dstMID, srcFolderID); err != nil {
		return err
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	return advanceFolderEID(tx, destFolderID, uint64(dstMID))
}

// dropMovedSourceIndex removes the IMAP index rows the moved message left
// behind. The object store renamed and reparented the message, but the index is
// a separate database with no cross-store cascade, so a source that was mail
// (indexed by AppendMessage) keeps a row pointing at the now-gone source id. The
// rows go, along with the cached eml, exactly as DeleteObject does. The
// destination is not re-indexed: the ICS upload path indexes only mail, like
// ImportMessageChange.
func (s *Store) dropMovedSourceIndex(srcMID int64) error {
	if _, err := s.idxdb.Exec(`DELETE FROM messages WHERE message_id=?`, srcMID); err != nil {
		return err
	}
	if _, err := s.idxdb.Exec(`DELETE FROM mapping WHERE message_id=?`, srcMID); err != nil {
		return err
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	_ = os.Remove(s.emlPath(midString(uint64(srcMID))))
	return nil
}

// ReadStateChange is one entry of a RopSynchronizationImportReadStateChanges
// request: the 22-byte source key naming a message and the read flag to apply.
type ReadStateChange struct {
	SourceKey []byte
	MarkRead  bool
}

// ImportReadStateChanges applies read-flag changes a client uploaded
// ([MS-OXCFXICS] 3.3.5.10). For each home-replica message in the folder whose flag
// actually differs (associated messages, which have no read state, are skipped) it
// records the new flag and a freshly allocated read change number, the version
// the contents delta diffs against a client's read set, and the first write path
// to record one. It returns those read change numbers (the upload state collector
// folds them into its read set). Foreign keys, absent ids, and no-op changes are
// skipped. The IMAP read flag is mirrored best-effort for any indexed message.
func (s *Store) ImportReadStateChanges(folderID int64, changes []ReadStateChange) ([]uint64, error) {
	home, err := s.replicaGUID()
	if err != nil {
		return nil, err
	}
	tx, err := s.objdb.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	readCNs, mirror, err := applyReadStateChanges(tx, folderID, changes, home)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := s.mirrorReadState(mirror); err != nil {
		return nil, err
	}
	if len(mirror) > 0 {
		s.publishChange("flags", 0, "")
	}
	return readCNs, nil
}

// applyReadStateChanges walks the uploaded changes, skipping the ones that name
// another store or land on nothing, and returns the allocated read change
// numbers alongside the changes to mirror into the IMAP index.
func applyReadStateChanges(tx *sql.Tx, folderID int64, changes []ReadStateChange, home mapi.GUID) ([]uint64, []appliedRead, error) {
	var readCNs []uint64
	var mirror []appliedRead
	for _, c := range changes {
		mid, foreign, err := parseSourceKeyMID(c.SourceKey, home)
		if err != nil {
			return nil, nil, err
		}
		if foreign {
			continue
		}
		rcn, want, applied, err := applyReadStateChange(tx, folderID, mid, c.MarkRead)
		if err != nil {
			return nil, nil, err
		}
		if !applied {
			continue
		}
		readCNs = append(readCNs, rcn)
		mirror = append(mirror, appliedRead{mid: mid, read: want})
	}
	return readCNs, mirror, nil
}

// mirrorReadState copies the applied read flags into the IMAP index. A message
// need not be indexed there (only mail is), so a no-op update is expected.
func (s *Store) mirrorReadState(mirror []appliedRead) error {
	for _, m := range mirror {
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		if _, err := s.idxdb.Exec(`UPDATE messages SET read=? WHERE message_id=?`, m.read, int64(m.mid)); err != nil {
			return err
		}
	}
	return nil
}

// appliedRead is one read-state change that actually landed, kept so the IMAP
// index can be mirrored after the transaction commits.
type appliedRead struct {
	mid  uint64
	read int
}

// applyReadStateChange records one message's new read flag and a freshly
// allocated read change number. applied is false when nothing was written: the
// message is absent from the folder, is associated (associated messages carry no
// read state), or is already in the requested state.
func applyReadStateChange(tx *sql.Tx, folderID int64, mid uint64, markRead bool) (rcn uint64, want int, applied bool, err error) {
	var cur, assoc int
	err = tx.QueryRow(
		`SELECT read_state, is_associated FROM messages WHERE message_id=? AND parent_fid=? AND is_deleted=0`,
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		int64(mid), folderID).Scan(&cur, &assoc)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	if markRead {
		want = 1
	}
	if assoc != 0 || cur == want {
		return 0, 0, false, nil
	}
	rcn, err = allocateCN(tx)
	if err != nil {
		return 0, 0, false, err
	}
	if _, err := tx.Exec(
		`UPDATE messages SET read_state=?, read_cn=? WHERE message_id=?`,
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		want, int64(rcn), int64(mid)); err != nil {
		return 0, 0, false, err
	}
	return rcn, want, true, nil
}

// ImportHierarchyChange creates or updates a folder a client uploaded
// ([MS-OXCFXICS] 3.3.5.10). hichyvals carries the fixed-order identity set the wire
// sends, parent source key, source key, last-modification time, change key,
// predecessor list, display name, and propvals any further folder properties. The
// source key names a home folder id; an empty parent source key parents the folder
// under the collector's root folder, otherwise the parent source key resolves to a
// home folder. An absent id is created at that id under the parent (its message-id
// range carved and the store cursor advanced past it); an existing id is updated
// and moved if its parent changed. Either way a fresh change number is allocated
// and the change key and predecessor list are derived from it, the client's are
// accepted but not stored, so v1 does no predecessor-list conflict detection. A
// foreign-replica source key (a cross-store import) is rejected in v1. It returns
// the folder id.
func (s *Store) ImportHierarchyChange(rootFID int64, hichyvals, propvals mapi.PropertyValues) (uint64, error) {
	home, err := s.replicaGUID()
	if err != nil {
		return 0, err
	}
	imp, err := parseHierarchyImport(rootFID, hichyvals, home)
	if err != nil {
		return 0, err
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	exists, err := s.FolderExists(int64(imp.fid))
	if err != nil {
		return 0, err
	}

	tx, err := s.objdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	cn, err := allocateCN(tx)
	if err != nil {
		return 0, err
	}
	ntNow := mapi.UnixToNTTime(time.Now())

	if err := s.writeImportedFolder(tx, imp, home, cn, ntNow, propvals, exists); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.publishChange("folder", cn, "")
	return imp.fid, nil
}

// hierarchyImport is the decoded identity of one imported folder: which folder
// it is, where it belongs, and the display name the client sent (hasName
// distinguishes an absent name from an empty one, since only an absent one
// leaves the stored name alone).
type hierarchyImport struct {
	fid      uint64
	parent   uint64
	dispName string
	hasName  bool
}

// parseHierarchyImport reads the source keys naming the folder and its parent.
// A key from another store is refused rather than mapped onto a local id, which
// would silently overwrite an unrelated folder.
func parseHierarchyImport(rootFID int64, hichyvals mapi.PropertyValues, home mapi.GUID) (hierarchyImport, error) {
	var imp hierarchyImport
	sk, ok := propBytes(hichyvals, mapi.PrSourceKey)
	if !ok {
		return imp, fmt.Errorf("objectstore: import hierarchy change missing PR_SOURCE_KEY")
	}
	fid, foreign, err := parseSourceKeyMID(sk, home)
	if err != nil {
		return imp, err
	}
	if foreign {
		return imp, fmt.Errorf("objectstore: cross-store folder import is not supported in v1")
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	imp = hierarchyImport{fid: fid, parent: uint64(rootFID)}
	if psk, ok := propBytes(hichyvals, mapi.PrParentSourceKey); ok && len(psk) > 0 {
		p, pforeign, err := parseSourceKeyMID(psk, home)
		if err != nil {
			return imp, err
		}
		if pforeign {
			return imp, fmt.Errorf("objectstore: cross-store folder parent is not supported in v1")
		}
		imp.parent = p
	}
	if v, ok := hichyvals.Get(mapi.PrDisplayName); ok {
		imp.dispName, _ = v.(string)
		imp.hasName = true
	}
	return imp, nil
}

// writeImportedFolder creates or updates the folder row and its property bag. An
// existing folder is re-parented and re-stamped; a new one is allocated its own
// message-id range first.
func (s *Store) writeImportedFolder(tx *sql.Tx, imp hierarchyImport, home mapi.GUID, cn, ntNow uint64, propvals mapi.PropertyValues, exists bool) error {
	bag, err := s.upsertFolderRow(tx, imp, home, cn, ntNow, propvals, exists)
	if err != nil {
		return err
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	return s.insertProps(tx, "folder_properties", "folder_id", int64(imp.fid), bag)
}

// upsertFolderRow writes the folders row and returns the property bag that goes
// with it.
func (s *Store) upsertFolderRow(tx *sql.Tx, imp hierarchyImport, home mapi.GUID, cn, ntNow uint64, propvals mapi.PropertyValues, exists bool) (mapi.PropertyValues, error) {
	if exists {
		if _, err := tx.Exec(
			`UPDATE folders SET parent_id=?, change_number=? WHERE folder_id=?`,
			// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
			int64(imp.parent), int64(cn), int64(imp.fid)); err != nil {
			return nil, err
		}
		return updatedFolderBag(home, cn, ntNow, imp.dispName, imp.hasName, propvals)
	}
	begin, end, err := allocateRange(tx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT INTO folders (folder_id, parent_id, change_number, cur_eid, max_eid) VALUES (?, ?, ?, ?, ?)`,
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		int64(imp.fid), int64(imp.parent), int64(cn), int64(begin), int64(end)); err != nil {
		return nil, err
	}
	if err := advanceStoreEID(tx, imp.fid); err != nil {
		return nil, err
	}
	return newFolderBag(tx, home, cn, ntNow, imp.dispName, propvals)
}

// newFolderBag builds the property bag for a freshly imported folder: the standard
// bag (counters, timestamps, computed change key and predecessor list) with the
// client's display name and container class, overlaid with any other client
// properties the store does not recompute.
func newFolderBag(tx *sql.Tx, replica mapi.GUID, cn, ntNow uint64, dispName string, propvals mapi.PropertyValues) (mapi.PropertyValues, error) {
	contClass := ""
	if v, ok := propvals.Get(mapi.PrContainerClass); ok {
		contClass, _ = v.(string)
	}
	hidden := false
	if v, ok := propvals.Get(mapi.PrAttrHidden); ok {
		hidden, _ = v.(bool)
	}
	bag, err := folderPropertyBag(tx, replica, ntNow, cn, dispName, contClass, true, hidden)
	if err != nil {
		return nil, err
	}
	for _, p := range propvals {
		if omitImportedFolderProp(p.Tag) {
			continue
		}
		bag.Set(p.Tag, p.Value)
	}
	return bag, nil
}

// updatedFolderBag builds the property changes for an existing folder: the
// recomputed change key and predecessor list, the modification time, the display
// name when the upload carried one, and the client's other non-recomputed
// properties. Stored properties not mentioned are left as they were.
func updatedFolderBag(replica mapi.GUID, cn, ntNow uint64, dispName string, hasName bool, propvals mapi.PropertyValues) (mapi.PropertyValues, error) {
	ck, err := changeKey(replica, cn)
	if err != nil {
		return nil, err
	}
	pcl, err := predecessorChangeList(replica, cn)
	if err != nil {
		return nil, err
	}
	bag := mapi.PropertyValues{
		{Tag: mapi.PrChangeKey, Value: ck},
		{Tag: mapi.PrPredecessorChangeList, Value: pcl},
		{Tag: mapi.PrLastModificationTime, Value: ntNow},
	}
	if hasName {
		bag = append(bag, mapi.TaggedPropVal{Tag: mapi.PrDisplayName, Value: dispName})
	}
	for _, p := range propvals {
		if omitImportedFolderProp(p.Tag) {
			continue
		}
		bag = append(bag, p)
	}
	return bag, nil
}

// omitImportedFolderProp reports whether a folder property from an upload is
// dropped: the identity keys and the change key / predecessor list the store
// derives itself, the counters and sizes the receiver recomputes, and named
// properties (which the hierarchy download strips).
func omitImportedFolderProp(tag mapi.PropTag) bool {
	switch tag {
	case mapi.PrSourceKey, mapi.PrParentSourceKey, mapi.PrChangeKey, mapi.PrPredecessorChangeList:
		return true
	}
	if _, ok := folderChangeOmit[tag]; ok {
		return true
	}
	return tag.ID() >= 0x8000
}

// advanceStoreEID bumps the store-level object-id cursor past a folder id imported
// into the current range, so a later allocation never reuses it. An id below the
// cursor (already allocated) or beyond the current range (a separately reserved
// block) matches nothing and is left alone.
func advanceStoreEID(q sqlExec, fid uint64) error {
	_, err := q.Exec(
		`UPDATE configurations SET config_value=?+1
		   WHERE config_id=? AND ?>=config_value
		     AND ?+1 <= (SELECT config_value FROM configurations WHERE config_id=?)`,
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		int64(fid), cfgCurrentEID, int64(fid), int64(fid), cfgMaximumEID)
	return err
}
