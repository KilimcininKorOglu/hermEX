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
// ROPs — the import-change ROP returns a message handle, then a FastTransfer
// destination fills it — so identity and content are kept apart here: this holds
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
// wire sends in fixed order — PR_SOURCE_KEY, PR_LAST_MODIFICATION_TIME,
// PR_CHANGE_KEY, PR_PREDECESSOR_CHANGE_LIST — of which the 22-byte source key
// names the message: a home-replica source key yields the message id, and the
// store decides new-vs-existing from it. The change key and predecessor list are
// accepted but not stored — the store derives both from the change number on
// download — so v1 does no predecessor-list conflict detection (last writer wins,
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
// content stream — the MESSAGECONTENT root form a client sends after an
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
		if c.frame != nil {
			return fmt.Errorf("objectstore: STARTRECIP nested inside marker %#x", c.frame.marker)
		}
		c.frame = &collectorFrame{marker: ics.MarkerStartRecip}
	case ics.MarkerEndToRecip:
		if c.frame == nil || c.frame.marker != ics.MarkerStartRecip {
			return fmt.Errorf("objectstore: ENDTORECIP without an open STARTRECIP")
		}
		c.um.msg.Recipients = append(c.um.msg.Recipients, c.frame.bag)
		c.frame = nil
	case ics.MarkerNewAttach:
		if c.frame != nil {
			return fmt.Errorf("objectstore: NEWATTACH nested inside marker %#x", c.frame.marker)
		}
		c.frame = &collectorFrame{marker: ics.MarkerNewAttach}
	case ics.MarkerEndAttach:
		if c.frame == nil || c.frame.marker != ics.MarkerNewAttach {
			return fmt.Errorf("objectstore: ENDATTACH without an open NEWATTACH")
		}
		c.um.msg.Attachments = append(c.um.msg.Attachments, oxcmail.Attachment{Props: c.frame.bag})
		c.frame = nil
	case ics.MarkerStartEmbed, ics.MarkerEndEmbed:
		return fmt.Errorf("objectstore: embedded-message upload is not supported in v1")
	case ics.MarkerStartMessage, ics.MarkerEndMessage:
		return fmt.Errorf("objectstore: message-list (bulk copy) upload is not supported in v1")
	default:
		return fmt.Errorf("objectstore: unexpected upload marker %#x", m)
	}
	return nil
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
	switch mapi.PropTag(uint32(tag)) {
	case mapi.PrMessageRecipients:
		c.um.msg.Recipients = nil
	case mapi.PrMessageAttachments:
		c.um.msg.Attachments = nil
	default:
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
// replaced wholesale — the messages-row delete cascades to its old property bag,
// recipients, attachments, and time-index row — and re-inserted. Either way a
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
	if um.isNew {
		if err := advanceFolderEID(tx, um.folderID, um.mid); err != nil {
			return 0, err
		}
	} else if _, err := tx.Exec(`DELETE FROM messages WHERE message_id=?`, int64(um.mid)); err != nil {
		return 0, err
	}

	assoc := 0
	if um.associated {
		assoc = 1
	}
	id := int64(um.mid)
	if _, err := tx.Exec(
		`INSERT INTO messages
		   (message_id, parent_fid, is_associated, change_number, read_state, message_size, mid_string)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, um.folderID, assoc, int64(cn), readState(um.msg.Props), messageSize(um.msg), midString(um.mid)); err != nil {
		return 0, err
	}
	if err := um.store.insertProps(tx, "message_properties", "message_id", id, um.msg.Props); err != nil {
		return 0, err
	}
	for _, rcpt := range um.msg.Recipients {
		res, err := tx.Exec(`INSERT INTO recipients (message_id) VALUES (?)`, id)
		if err != nil {
			return 0, err
		}
		rid, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		if err := um.store.insertProps(tx, "recipients_properties", "recipient_id", rid, rcpt); err != nil {
			return 0, err
		}
	}
	for _, att := range um.msg.Attachments {
		res, err := tx.Exec(`INSERT INTO attachments (message_id) VALUES (?)`, id)
		if err != nil {
			return 0, err
		}
		aid, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		if err := um.store.insertProps(tx, "attachment_properties", "attachment_id", aid, att.Props); err != nil {
			return 0, err
		}
	}
	if err := insertMsgTime(tx, um.folderID, id, um.msg.Props); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	op := "modify"
	if um.isNew {
		op = "create"
	}
	um.store.publishChange(op, cn, midString(um.mid))
	return um.mid, nil
}

// advanceFolderEID bumps a folder's allocation cursor past an id imported into its
// reserved range, so a later server-side allocation never reuses it. An id outside
// the folder's current range (a client id drawn from a separately reserved block)
// matches nothing and is left alone.
func advanceFolderEID(q sqlExec, folderID int64, mid uint64) error {
	_, err := q.Exec(
		`UPDATE folders SET cur_eid=?+1 WHERE folder_id=? AND ?>=cur_eid AND ?<=max_eid`,
		int64(mid), folderID, int64(mid), int64(mid))
	return err
}

// ImportDeletes removes the messages a client reports gone from a folder
// ([MS-OXCFXICS] 3.3.5.10). Each source key is a 22-byte XID; a home-replica one
// names a message id, which is deleted when it is present in the folder.
// Foreign-replica keys (cross-store) and ids absent from the folder are skipped,
// so the operation is idempotent. v1 always hard-deletes — the store keeps no
// soft-delete state — so the soft/hard distinction the wire carries is a
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
		err = s.objdb.QueryRow(`SELECT 1 FROM messages WHERE message_id=? AND parent_fid=?`, int64(mid), folderID).Scan(&present)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
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
	if dstMID != srcMID {
		if _, err := tx.Exec(`DELETE FROM messages WHERE message_id=?`, dstMID); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(
		`UPDATE messages SET message_id=?, parent_fid=?, change_number=?, mid_string=? WHERE message_id=?`,
		dstMID, destFolderID, int64(cn), midString(uint64(dstMID)), srcMID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(
		`UPDATE msgtime_index SET folder_id=? WHERE message_id=? AND folder_id=?`,
		destFolderID, dstMID, srcFolderID); err != nil {
		return false, err
	}
	if err := advanceFolderEID(tx, destFolderID, uint64(dstMID)); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}

	// The object store renamed and reparented the message; the IMAP index is a
	// separate database with no cross-store cascade, so a source that was mail
	// (indexed by AppendMessage) keeps a row pointing at the now-gone source id.
	// Drop those rows so an IMAP view does not show a ghost in the source folder,
	// and orphan its cached eml, exactly as DeleteObject does. The destination is
	// not re-indexed: the ICS upload path indexes only mail, as ImportMessageChange.
	srcMidString := midString(uint64(srcMID))
	if _, err := s.idxdb.Exec(`DELETE FROM messages WHERE message_id=?`, srcMID); err != nil {
		return assoc != 0, err
	}
	if _, err := s.idxdb.Exec(`DELETE FROM mapping WHERE message_id=?`, srcMID); err != nil {
		return assoc != 0, err
	}
	_ = os.Remove(s.emlPath(srcMidString))

	s.publishChange("create", cn, midString(uint64(dstMID)))
	return assoc != 0, nil
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
// records the new flag and a freshly allocated read change number — the version
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

	type applied struct {
		mid  uint64
		read int
	}
	var readCNs []uint64
	var mirror []applied
	for _, c := range changes {
		mid, foreign, err := parseSourceKeyMID(c.SourceKey, home)
		if err != nil {
			return nil, err
		}
		if foreign {
			continue
		}
		var cur, assoc int
		err = tx.QueryRow(
			`SELECT read_state, is_associated FROM messages WHERE message_id=? AND parent_fid=? AND is_deleted=0`,
			int64(mid), folderID).Scan(&cur, &assoc)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if assoc != 0 {
			continue // associated messages carry no read state
		}
		want := 0
		if c.MarkRead {
			want = 1
		}
		if cur == want {
			continue // already in the requested state
		}
		rcn, err := allocateCN(tx)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(
			`UPDATE messages SET read_state=?, read_cn=? WHERE message_id=?`,
			want, int64(rcn), int64(mid)); err != nil {
			return nil, err
		}
		readCNs = append(readCNs, rcn)
		mirror = append(mirror, applied{mid: mid, read: want})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, m := range mirror {
		// The message need not be in the IMAP index (only mail is); a no-op update
		// there is harmless.
		if _, err := s.idxdb.Exec(`UPDATE messages SET read=? WHERE message_id=?`, m.read, int64(m.mid)); err != nil {
			return nil, err
		}
	}
	if len(mirror) > 0 {
		s.publishChange("flags", 0, "")
	}
	return readCNs, nil
}

// ImportHierarchyChange creates or updates a folder a client uploaded
// ([MS-OXCFXICS] 3.3.5.10). hichyvals carries the fixed-order identity set the wire
// sends — parent source key, source key, last-modification time, change key,
// predecessor list, display name — and propvals any further folder properties. The
// source key names a home folder id; an empty parent source key parents the folder
// under the collector's root folder, otherwise the parent source key resolves to a
// home folder. An absent id is created at that id under the parent (its message-id
// range carved and the store cursor advanced past it); an existing id is updated
// and moved if its parent changed. Either way a fresh change number is allocated
// and the change key and predecessor list are derived from it — the client's are
// accepted but not stored — so v1 does no predecessor-list conflict detection. A
// foreign-replica source key (a cross-store import) is rejected in v1. It returns
// the folder id.
func (s *Store) ImportHierarchyChange(rootFID int64, hichyvals, propvals mapi.PropertyValues) (uint64, error) {
	home, err := s.replicaGUID()
	if err != nil {
		return 0, err
	}
	sk, ok := propBytes(hichyvals, mapi.PrSourceKey)
	if !ok {
		return 0, fmt.Errorf("objectstore: import hierarchy change missing PR_SOURCE_KEY")
	}
	fid, foreign, err := parseSourceKeyMID(sk, home)
	if err != nil {
		return 0, err
	}
	if foreign {
		return 0, fmt.Errorf("objectstore: cross-store folder import is not supported in v1")
	}
	parent := uint64(rootFID)
	if psk, ok := propBytes(hichyvals, mapi.PrParentSourceKey); ok && len(psk) > 0 {
		p, pforeign, err := parseSourceKeyMID(psk, home)
		if err != nil {
			return 0, err
		}
		if pforeign {
			return 0, fmt.Errorf("objectstore: cross-store folder parent is not supported in v1")
		}
		parent = p
	}
	dispName, hasName := "", false
	if v, ok := hichyvals.Get(mapi.PrDisplayName); ok {
		dispName, _ = v.(string)
		hasName = true
	}

	exists, err := s.FolderExists(int64(fid))
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

	if exists {
		if _, err := tx.Exec(
			`UPDATE folders SET parent_id=?, change_number=? WHERE folder_id=?`,
			int64(parent), int64(cn), int64(fid)); err != nil {
			return 0, err
		}
		bag, err := updatedFolderBag(home, cn, ntNow, dispName, hasName, propvals)
		if err != nil {
			return 0, err
		}
		if err := s.insertProps(tx, "folder_properties", "folder_id", int64(fid), bag); err != nil {
			return 0, err
		}
	} else {
		begin, end, err := allocateRange(tx)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(
			`INSERT INTO folders (folder_id, parent_id, change_number, cur_eid, max_eid) VALUES (?, ?, ?, ?, ?)`,
			int64(fid), int64(parent), int64(cn), int64(begin), int64(end)); err != nil {
			return 0, err
		}
		if err := advanceStoreEID(tx, fid); err != nil {
			return 0, err
		}
		bag, err := newFolderBag(tx, home, cn, ntNow, dispName, propvals)
		if err != nil {
			return 0, err
		}
		if err := s.insertProps(tx, "folder_properties", "folder_id", int64(fid), bag); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.publishChange("folder", cn, "")
	return fid, nil
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
		int64(fid), cfgCurrentEID, int64(fid), int64(fid), cfgMaximumEID)
	return err
}
