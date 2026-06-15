package objectstore

import (
	"bytes"
	"database/sql"
	"fmt"

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
