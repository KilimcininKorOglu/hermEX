package rop

import (
	"errors"
	"strings"
	"time"

	"hermex/internal/ext"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// RECIPIENT_ROW flags ([MS-OXCDATA] 2.8.3.1 RecipientFlags). The low three bits
// select the address kind; the remaining bits gate the optional fields.
const (
	recipientRowEmail         uint16 = 0x0008
	recipientRowDisplay       uint16 = 0x0010
	recipientRowTransmittable uint16 = 0x0020
	recipientRowResponsible   uint16 = 0x0080
	recipientRowNonRich       uint16 = 0x0100
	recipientRowUnicode       uint16 = 0x0200
	recipientRowSimple        uint16 = 0x0400
	recipientRowOutOfStandard uint16 = 0x8000
)

// RECIPIENT_ROW address kinds (flags & 0x0007).
const (
	addrKindNoType uint16 = 0x0
	addrKindX500DN uint16 = 0x1
	addrKindSMTP   uint16 = 0x3
	addrKindDList1 uint16 = 0x6
	addrKindDList2 uint16 = 0x7
)

// errRecipientFraming marks a MODIFYRECIPIENT_ROW whose fixed framing (row id,
// type, size) could not be read, an unrecoverable desync that ends the batch,
// unlike a malformed row body, which is skipped (the row was size-bounded).
var errRecipientFraming = errors.New("rop: malformed recipient row framing")

// ropCreateMessage handles RopCreateMessage ([MS-OXCMSG] 2.2.3.1): it opens an
// in-memory message under the output handle, to be filled by SetProperties /
// ModifyRecipients and persisted by SaveChangesMessage. The response carries no
// message id (HasMessageId 0), the id is allocated when the message is saved.
func (s *Session) ropCreateMessage(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, folderEID, associated, ok := pullCreateMessageRequest(p)
	if !ok {
		return false
	}
	parent, fid, ok := s.createMessageTarget(out, handles, hindex, ohindex, folderEID)
	if !ok {
		return true
	}
	props := mapi.PropertyValues{}
	if associated != 0 {
		// Mark the message folder-associated (FAI); the store reads PidTagAssociated
		// at save and records it on the message row, so a hidden setting/rule message
		// is not stored as a visible item.
		props.Set(mapi.PrAssociated, true)
	}
	h := s.alloc(&object{
		kind:   kindNewMessage,
		store:  parent.store,
		newMsg: &newMessageState{folderID: fid, props: props},
	})
	setHandle(handles, ohindex, h)

	out.Uint8(ropCreateMessage)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // HasMessageId: id is assigned at SaveChangesMessage
	return true
}

// pullCreateMessageRequest reads the RopCreateMessage request fields. The code
// page is discarded: this session speaks Unicode throughout.
func pullCreateMessageRequest(p *ext.Pull) (ohindex uint8, folderEID uint64, associated uint8, ok bool) {
	ohindex, e1 := p.Uint8()    // OutputHandleIndex
	_, e2 := p.Uint16()         // CodePageId
	folderEID, e3 := p.Uint64() // FolderId
	associated, e4 := p.Uint8() // AssociatedFlag
	return ohindex, folderEID, associated, e1 == nil && e2 == nil && e3 == nil && e4 == nil
}

// createMessageTarget resolves the parent folder a new message is composed in
// and gates it on the Create right, which is this compose handle's only check:
// the SetProperties/ModifyRecipients/SaveChanges that fill the message inherit
// it. ok=false means the response was already written.
func (s *Session) createMessageTarget(out *ext.Push, handles []uint32, hindex, ohindex uint8, folderEID uint64) (*object, int64, bool) {
	parent := s.get(handleAt(handles, hindex))
	if parent == nil || parent.store == nil {
		writeErr(out, ropCreateMessage, ohindex, ecError)
		return nil, 0, false
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	fid := int64(mapi.EID(folderEID).GCValue())
	exists, err := parent.store.FolderExists(fid)
	if err != nil {
		writeErr(out, ropCreateMessage, ohindex, ecError)
		return nil, 0, false
	}
	if !exists {
		writeErr(out, ropCreateMessage, ohindex, ecNotFound)
		return nil, 0, false
	}
	if s.denyWrite(out, ropCreateMessage, ohindex, parent.store, fid, mapi.FrightsCreate) {
		return nil, 0, false
	}
	return parent, fid, true
}

// ropSetProperties handles RopSetProperties ([MS-OXCPRPT] 2.2.2.5): it merges
// the request's TPROPVAL_ARRAY into the open message's property bag. The values
// occupy a length-bounded region, read from an isolated slice so trailing bytes
// in that region cannot be over-read. It supports both a message being composed
// (kindNewMessage) and an existing message opened for edit (kindMessage), whose
// changes are buffered in pendingProps and flushed by SaveChangesMessage,
// MAPI's transactional semantics keep an edit invisible until that save. It
// reports no property problems.
func (s *Session) ropSetProperties(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	size, e1 := p.Uint16() // PropertyValueSize
	if e1 != nil {
		return false
	}
	body, e2 := p.Raw(int(size))
	if e2 != nil {
		return false
	}
	propvals, e3 := ext.NewPull(body, ext.FlagUTF16).PropertyValues()
	if e3 != nil {
		return false
	}
	obj := s.get(handleAt(handles, hindex))
	if obj == nil {
		writeErr(out, ropSetProperties, hindex, ecError)
		return true
	}
	if !s.applySetProperties(out, obj, hindex, propvals) {
		return true
	}

	out.Uint8(ropSetProperties)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(0) // PropertyProblemCount
	return true
}

// applySetProperties merges the values into whichever bag the open object keeps
// its edits in. It reports false when the response was already written: an
// object kind that holds no writable bag, or an edit the folder rights refuse.
func (s *Session) applySetProperties(out *ext.Push, obj *object, hindex uint8, propvals []mapi.TaggedPropVal) bool {
	switch obj.kind {
	case kindNewMessage:
		setAll(&obj.newMsg.props, propvals)
	case kindMessage:
		// Editing an existing message requires EditAny on its folder (the read-mode
		// open only proved ReadAny). Compose (kindNewMessage) and attachment writes
		// are gated at their own create chokepoints.
		if s.denyWrite(out, ropSetProperties, hindex, obj.store, obj.folderID, mapi.FrightsEditAny) {
			return false
		}
		obj.pendingDeletes = setAllOverridingDeletes(&obj.pendingProps, obj.pendingDeletes, propvals)
	case kindAttachWrite:
		obj.attachW.pendingDeletes = setAllOverridingDeletes(&obj.attachW.pending, obj.attachW.pendingDeletes, propvals)
	case kindEmbedded:
		// A composed embedded message buffers its edits in memory; they are exported
		// into the parent attachment when SaveChangesMessage runs.
		setAll(&obj.embedded.msg.Props, propvals)
	default:
		writeErr(out, ropSetProperties, hindex, ecError)
		return false
	}
	return true
}

// setAll merges every tagged value into bag.
func setAll(bag *mapi.PropertyValues, propvals []mapi.TaggedPropVal) {
	for _, tv := range propvals {
		bag.Set(tv.Tag, tv.Value)
	}
}

// setAllOverridingDeletes merges the values and drops each tag from the buffered
// deletes, because a set supersedes a delete of the same tag within one edit
// session: SaveChangesMessage must not delete the row it just inserted. It is
// the mirror of deleteProperties dropping a buffered set.
func setAllOverridingDeletes(bag *mapi.PropertyValues, deletes []mapi.PropTag, propvals []mapi.TaggedPropVal) []mapi.PropTag {
	for _, tv := range propvals {
		bag.Set(tv.Tag, tv.Value)
		deletes = dropDeleteTag(deletes, tv.Tag)
	}
	return deletes
}

// ropModifyRecipients handles RopModifyRecipients ([MS-OXCMSG] 2.2.3.5): it
// replaces the open message's recipient table with the request's rows. Each row
// is a MODIFYRECIPIENT_ROW carrying a size-bounded RECIPIENT_ROW; the rows are
// parsed before the target handle is resolved so the batch stays aligned even
// when the handle is wrong. v1 implements full-set replace, not incremental
// modify-by-rowid.
func (s *Session) ropModifyRecipients(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	columns, e1 := p.PropTags() // RecipientColumns
	count, e2 := p.Uint16()     // RowCount
	if e1 != nil || e2 != nil {
		return false
	}
	var recipients []mapi.PropertyValues
	for range int(count) {
		bag, ok, err := pullModifyRecipientBag(p, columns)
		if err != nil {
			return false // framing desync, the batch can no longer be located
		}
		if ok {
			recipients = append(recipients, bag)
		}
	}
	obj := s.get(handleAt(handles, hindex))
	if obj == nil || obj.kind != kindNewMessage {
		writeErr(out, ropModifyRecipients, hindex, ecError)
		return true
	}
	obj.newMsg.recipients = recipients

	out.Uint8(ropModifyRecipients)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	return true
}

// ropSaveChangesMessage handles RopSaveChangesMessage ([MS-OXCMSG] 2.2.3.3): it
// persists the composed message via objectstore.CreateMessage and returns the
// new message id as an EID. The message object is addressed by the body's
// InputHandleIndex (ihindex2), distinct from the common-header ResponseHandleIndex
// the response echoes. A second save updates the stored properties in place
// rather than inserting a duplicate.
func (s *Session) ropSaveChangesMessage(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ihindex2, ok := pullSaveChangesRequest(p)
	if !ok {
		return false
	}
	obj := s.get(handleAt(handles, ihindex2))
	if obj == nil || obj.store == nil {
		writeErr(out, ropSaveChangesMessage, hindex, ecError)
		return true
	}
	switch obj.kind {
	case kindUploadMessage:
		s.saveUploadedMessage(out, obj, hindex, ihindex2)
	case kindMessage:
		s.saveOpenedMessage(out, obj, hindex, ihindex2)
	case kindEmbedded:
		saveEmbeddedMessage(out, obj, hindex, ihindex2, handleAt(handles, ihindex2))
	case kindNewMessage:
		saveComposedMessage(out, obj, hindex, ihindex2)
	default:
		writeErr(out, ropSaveChangesMessage, hindex, ecError)
	}
	return true
}

// pullSaveChangesRequest reads the RopSaveChangesMessage request fields. The
// save flags are discarded: this implementation always saves.
func pullSaveChangesRequest(p *ext.Pull) (ihindex uint8, ok bool) {
	ihindex, e1 := p.Uint8() // InputHandleIndex (indexes the message object)
	_, e2 := p.Uint8()       // SaveFlags
	return ihindex, e1 == nil && e2 == nil
}

// writeSaveChangesOK appends the success response, whose MessageId is an EID
// built from the caller's global counter value.
func writeSaveChangesOK(out *ext.Push, hindex, ihindex uint8, gcValue uint64) {
	out.Uint8(ropSaveChangesMessage)
	out.Uint8(hindex) // ResponseHandleIndex (echoed in the header)
	out.Uint32(ecSuccess)
	out.Uint8(ihindex) // InputHandleIndex (echoed in the body)
	out.Uint64(uint64(mapi.MakeEIDEx(1, gcValue)))
}

// saveUploadedMessage commits an ICS-imported message's FastTransfer-uploaded
// body, the same point a composed message is saved ([MS-OXCFXICS] 3.3.5.6).
func (s *Session) saveUploadedMessage(out *ext.Push, obj *object, hindex, ihindex uint8) {
	if obj.uploadMsg == nil {
		writeErr(out, ropSaveChangesMessage, hindex, ecError)
		return
	}
	mid, err := obj.uploadMsg.Commit()
	if err != nil {
		writeErr(out, ropSaveChangesMessage, hindex, ecError)
		return
	}
	// A FastTransfer upload is client-supplied content that never passes through
	// delivery. Its attachments only exist as stored objects once the commit has
	// assembled them, so the scan runs here and a hit removes the message again:
	// it never becomes readable to a client, and the quarantine keeps the evidence.
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	if s.scanUploadedMessage(obj.store, int64(mid)) {
		writeErr(out, ropSaveChangesMessage, hindex, ecAccessDenied)
		return
	}
	writeSaveChangesOK(out, hindex, ihindex, mid)
}

// saveOpenedMessage flushes an existing message's buffered property changes in
// place, reallocating the change number so ICS observes the edit. A pending
// property change or a touched flag (set when an attachment was added or
// deleted) bumps the change number; with neither, the save is a no-op success
// (no spurious bump), matching the untouched-message early-out. An
// attachment-only change carries no pending properties, so
// ModifyMessageProperties runs with an empty bag and advances only the change
// number.
func (s *Session) saveOpenedMessage(out *ext.Push, obj *object, hindex, ihindex uint8) {
	// Persisting an edit to an existing message requires EditAny on its folder.
	if s.denyWrite(out, ropSaveChangesMessage, hindex, obj.store, obj.folderID, mapi.FrightsEditAny) {
		return
	}
	if obj.hasBufferedChanges() {
		if err := obj.store.ModifyMessageProperties(obj.messageID, obj.pendingProps, obj.pendingDeletes...); err != nil {
			writeErr(out, ropSaveChangesMessage, hindex, ecError)
			return
		}
		obj.pendingProps = nil
		obj.pendingDeletes = nil
		obj.touched = false
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	writeSaveChangesOK(out, hindex, ihindex, uint64(obj.messageID))
}

// hasBufferedChanges reports whether an opened message has anything to flush.
func (o *object) hasBufferedChanges() bool {
	return len(o.pendingProps) > 0 || len(o.pendingDeletes) > 0 || o.touched
}

// saveEmbeddedMessage persists a composed embedded message by exporting it back
// into its parent attachment: the export bytes, method, and MIME tag are staged
// into the attachment's pending bag, which the client's SaveChangesAttachment
// then writes through the ordinary attachment path. A read-only embedded message
// (opened over an existing attachment) has no write-back target and cannot be
// saved.
func saveEmbeddedMessage(out *ext.Push, obj *object, hindex, ihindex uint8, handle uint32) {
	emb := obj.embedded
	if emb == nil || emb.writeback == nil {
		writeErr(out, ropSaveChangesMessage, hindex, ecNotSupported)
		return
	}
	raw, err := oxcmail.Export(emb.msg, oxcmail.Options{})
	if err != nil {
		writeErr(out, ropSaveChangesMessage, hindex, ecError)
		return
	}
	emb.writeback.pending.Set(mapi.PrAttachMethod, int32(mapi.AttachEmbeddedMsg))
	emb.writeback.pending.Set(mapi.PrAttachMimeTag, "message/rfc822")
	emb.writeback.pending.Set(mapi.PrAttachDataBin, raw)
	writeSaveChangesOK(out, hindex, ihindex, uint64(handle))
}

// saveComposedMessage persists a message built through the compose handle,
// creating it on the first save and editing it in place on any later one.
func saveComposedMessage(out *ext.Push, obj *object, hindex, ihindex uint8) {
	nm := obj.newMsg
	id, err := persistComposedMessage(obj.store, nm)
	if err != nil {
		writeErr(out, ropSaveChangesMessage, hindex, ecError)
		return
	}
	nm.saved = true
	nm.savedID = id
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	writeSaveChangesOK(out, hindex, ihindex, uint64(id))
}

// persistComposedMessage writes the composed message and returns its id. A
// re-save after the first persist is an in-place edit through the same path an
// opened message uses, rather than a pure upsert, which would leave the message
// looking unchanged to ICS.
func persistComposedMessage(store *objectstore.Store, nm *newMessageState) (int64, error) {
	if nm.saved {
		return nm.savedID, store.ModifyMessageProperties(nm.savedID, nm.props)
	}
	// The attachments staged during compose are written together with the message.
	atts := make([]oxcmail.Attachment, len(nm.attachments))
	for i, a := range nm.attachments {
		atts[i] = oxcmail.Attachment{Props: a.props}
	}
	return store.CreateMessage(nm.folderID, &oxcmail.Message{
		Props:       nm.props,
		Recipients:  nm.recipients,
		Attachments: atts,
	})
}

// pullModifyRecipientBag parses one MODIFYRECIPIENT_ROW ([MS-OXCMSG] 2.2.3.5.2)
// into a recipient property bag. It returns whether the row should be included:
// false for a removal marker (RecipientRowSize 0) or an unparseable/unsupported
// row. A non-nil error means the fixed framing itself could not be read, which
// desyncs the batch; a bad row body does not, since the row is size-bounded.
func pullModifyRecipientBag(p *ext.Pull, columns []mapi.PropTag) (mapi.PropertyValues, bool, error) {
	rowID, e1 := p.Uint32()   // RowId
	rcptType, e2 := p.Uint8() // RecipientType
	rowSize, e3 := p.Uint16() // RecipientRowSize
	if e1 != nil || e2 != nil || e3 != nil {
		return nil, false, errRecipientFraming
	}
	if rowSize == 0 {
		return nil, false, nil // removal marker; unused under full-set replace
	}
	// Slice the RECIPIENT_ROW off by its declared size so the parent stream is
	// authoritatively re-synced regardless of how the row body decodes.
	rowBytes, err := p.Raw(int(rowSize))
	if err != nil {
		return nil, false, errRecipientFraming
	}
	bag, ok := pullRecipientRow(ext.NewPull(rowBytes, ext.FlagUTF16), columns)
	if !ok {
		return nil, false, nil
	}
	// #nosec G115 -- the signed and unsigned views of the same 32 bits
	bag.Set(mapi.PrRowid, int32(rowID))
	bag.Set(mapi.PrRecipientType, int32(rcptType))
	return bag, true, nil
}

// pullRecipientRow parses a RECIPIENT_ROW ([MS-OXCDATA] 2.8.3.2) from an isolated
// sub-pull, mapping its flag-driven fields and trailing PROPERTY_ROW to a
// recipient property bag (mirroring the reference recipient->propvals mapping).
// It reports include=false on any parse failure or an unsupported address kind;
// because the row was sliced off by its size, a skipped row never desyncs the
// batch. The unicode flag types only the flag-driven name/email fields, not the
// trailing PROPERTY_ROW (whose values are typed by each column's proptag).
func pullRecipientRow(p *ext.Pull, columns []mapi.PropTag) (mapi.PropertyValues, bool) {
	flags, err := p.Uint16()
	if err != nil {
		return nil, false
	}
	row := recipientRow{flags: flags, addrKind: flags & 0x0007}
	rd := rowReader{p: p, unicode: flags&recipientRowUnicode != 0}
	if !row.pullAddress(rd) || !row.pullNames(rd) {
		return nil, false
	}
	bag, ok := row.props()
	if !ok {
		return nil, false
	}
	if !pullRecipientColumns(p, columns, &bag) {
		return nil, false
	}
	return bag, true
}

// rowReader reads a RECIPIENT_ROW string in the encoding the row's flags select:
// the whole row is either Unicode or ASCII, decided once by the flag word.
type rowReader struct {
	p       *ext.Pull
	unicode bool
}

func (r rowReader) str() (string, error) {
	if r.unicode {
		return r.p.Unicode()
	}
	return r.p.String8()
}

// recipientRow is the flag-driven part of a RECIPIENT_ROW, decoded but not yet
// turned into properties. Each have* field says whether the flags called for
// that member at all, which is not the same as it being empty.
type recipientRow struct {
	flags    uint16
	addrKind uint16

	x500dn       string
	addrType     string
	mailAddr     string
	displayName  string
	transmitName string

	haveX500     bool
	haveAddrType bool
	haveMail     bool
	haveDisplay  bool
	haveTransmit bool
}

// pullAddress reads the members the address kind dictates: the X500 DN for an
// EX recipient, the entry id and search key of a distribution list (both
// discarded in v1), and the out-of-standard address type of an untyped one.
func (r *recipientRow) pullAddress(rd rowReader) bool {
	if !r.pullAddressKind(rd) {
		return false
	}
	if r.addrKind == addrKindNoType && r.flags&recipientRowOutOfStandard != 0 {
		var err error
		if r.addrType, err = rd.p.String8(); err != nil { // AddressType (always ASCII)
			return false
		}
		r.haveAddrType = true
	}
	return true
}

// pullAddressKind reads the members only one address kind carries. Every other
// kind contributes nothing here and is judged in props instead.
func (r *recipientRow) pullAddressKind(rd rowReader) bool {
	switch r.addrKind {
	case addrKindX500DN:
		return r.pullX500(rd)
	case addrKindDList1, addrKindDList2:
		return skipDistributionList(rd.p)
	}
	return true
}

// pullX500 reads an EX recipient's DN, preceded by the two bytes v1 discards.
func (r *recipientRow) pullX500(rd rowReader) bool {
	var err error
	if _, err = rd.p.Uint8(); err != nil { // PrefixUsed
		return false
	}
	if _, err = rd.p.Uint8(); err != nil { // DisplayType
		return false
	}
	if r.x500dn, err = rd.p.String8(); err != nil { // X500DN (always ASCII)
		return false
	}
	r.haveX500 = true
	return true
}

// skipDistributionList consumes a distribution list's entry id and search key,
// which v1 does not model but must read to keep the stream framed.
func skipDistributionList(p *ext.Pull) bool {
	if _, err := p.Bin(); err != nil { // EntryId
		return false
	}
	_, err := p.Bin() // SearchKey
	return err == nil
}

// pullNames reads the optional name members, each present only when its flag is
// set. SimpleDisplayName is read to keep the stream framed and then discarded.
func (r *recipientRow) pullNames(rd rowReader) bool {
	var err error
	if r.flags&recipientRowEmail != 0 {
		if r.mailAddr, err = rd.str(); err != nil {
			return false
		}
		r.haveMail = true
	}
	if r.flags&recipientRowDisplay != 0 {
		if r.displayName, err = rd.str(); err != nil {
			return false
		}
		r.haveDisplay = true
	}
	if r.flags&recipientRowSimple != 0 {
		if _, err = rd.str(); err != nil { // SimpleDisplayName (v1 ignores)
			return false
		}
	}
	if r.flags&recipientRowTransmittable != 0 {
		if r.transmitName, err = rd.str(); err != nil {
			return false
		}
		r.haveTransmit = true
	}
	return true
}

// props turns the decoded row into its property bag. ok is false for an address
// kind v1 does not support, and for an EX recipient whose DN never arrived.
func (r *recipientRow) props() (mapi.PropertyValues, bool) {
	bag := mapi.PropertyValues{}
	bag.Set(mapi.PrResponsibility, r.flags&recipientRowResponsible != 0)
	bag.Set(mapi.PrSendRichInfo, r.flags&recipientRowNonRich != 0)
	if r.haveTransmit {
		bag.Set(mapi.PrTransmitableDisplayName, r.transmitName)
	}
	if r.haveDisplay {
		bag.Set(mapi.PrDisplayName, r.displayName)
	}
	if r.haveMail {
		bag.Set(mapi.PrEmailAddress, r.mailAddr)
	}
	switch r.addrKind {
	case addrKindNoType:
		if r.haveAddrType {
			bag.Set(mapi.PrAddrType, r.addrType)
		}
	case addrKindX500DN:
		if !r.haveX500 {
			return nil, false
		}
		bag.Set(mapi.PrAddrType, "EX")
		bag.Set(mapi.PrEmailAddress, r.x500dn)
	case addrKindSMTP:
		bag.Set(mapi.PrAddrType, "SMTP")
	default:
		return nil, false // MSMAIL / FAX / personal distribution list, unsupported in v1
	}
	return bag, true
}

// pullRecipientColumns merges the trailing PROPERTY_ROW over the first
// RecipientColumnCount columns; its values (e.g. PR_SMTP_ADDRESS) land after the
// flag-driven fields, so they win where both carry the same property.
func pullRecipientColumns(p *ext.Pull, columns []mapi.PropTag, bag *mapi.PropertyValues) bool {
	count, err := p.Uint16()
	if err != nil {
		return false
	}
	if int(count) > len(columns) {
		return false
	}
	return pullPropertyRow(p, columns[:count], bag) == nil
}

// pullPropertyRow parses a PROPERTY_ROW ([MS-OXCDATA] 2.8.1) over columns and
// merges each present value into bag. It is the inverse of buildPropertyRow: a
// flag byte selects the NONE form (a bare value per column) or the FLAGGED form
// (a FLAGGED_PROPVAL per column, where unavailable/error columns carry no value).
// Each value's type comes from its column proptag.
func pullPropertyRow(p *ext.Pull, columns []mapi.PropTag, bag *mapi.PropertyValues) error {
	flag, err := p.Uint8()
	if err != nil {
		return err
	}
	switch flag {
	case propertyRowNone:
		return pullBareRow(p, columns, bag)
	case propertyRowFlagged:
		return pullFlaggedRow(p, columns, bag)
	}
	return errRecipientFraming
}

// pullBareRow reads the NONE form: one value per column, every column present.
func pullBareRow(p *ext.Pull, columns []mapi.PropTag, bag *mapi.PropertyValues) error {
	for _, col := range columns {
		v, err := p.PropValue(col.Type())
		if err != nil {
			return err
		}
		bag.Set(col, v)
	}
	return nil
}

// pullFlaggedRow reads the FLAGGED form: each column carries a marker saying
// whether its value is present, absent, or replaced by an error code.
func pullFlaggedRow(p *ext.Pull, columns []mapi.PropTag, bag *mapi.PropertyValues) error {
	for _, col := range columns {
		marker, err := p.Uint8()
		if err != nil {
			return err
		}
		if err := pullFlaggedValue(p, col, marker, bag); err != nil {
			return err
		}
	}
	return nil
}

// pullFlaggedValue reads one FLAGGED_PROPVAL body, whichever form its marker
// selected.
func pullFlaggedValue(p *ext.Pull, col mapi.PropTag, marker uint8, bag *mapi.PropertyValues) error {
	switch marker {
	case mapi.FlaggedAvailable:
		v, err := p.PropValue(col.Type())
		if err != nil {
			return err
		}
		bag.Set(col, v)
	case mapi.FlaggedUnavailable:
		// no value present for this column
	case mapi.FlaggedError:
		if _, err := p.Uint32(); err != nil { // error code, discarded
			return err
		}
	default:
		return errRecipientFraming
	}
	return nil
}

// ropSubmitMessage handles RopSubmitMessage ([MS-OXOMSG] 2.2.3.1.1 /
// [MS-OXCROPS] 2.2.7.1.1). Mirroring the reference submit path, it requires the
// composed message to have been saved and to carry at least one routable
// recipient, stamps the sender identity the wire copy needs, exports the message
// through oxcmail, hands it to the MTA bridge, files a copy in Sent Items, and
// consumes the source draft so the submitted message is not left duplicated.
// Single input handle; the response is the bare header.
func (s *Session) ropSubmitMessage(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	if _, err := p.Uint8(); err != nil { // SubmitFlags (PreProcess/NeedsSpooler, v1 ignores)
		return false
	}
	obj := s.get(handleAt(handles, hindex))
	if obj == nil || obj.kind != kindNewMessage || obj.newMsg == nil {
		writeErr(out, ropSubmitMessage, hindex, ecNotFound)
		return true
	}
	representing, sender, ok := s.authorizeSubmit(out, obj, ropSubmitMessage, hindex)
	if !ok {
		return true
	}
	nm := obj.newMsg
	raw, err := s.deliverComposed(nm, representing, sender)
	if err != nil {
		writeErr(out, ropSubmitMessage, hindex, noRecipientOrError(err))
		return true
	}
	// Delivery has succeeded. The Sent Items copy is filed in the mailbox the message
	// was sent from, for a send-on-behalf submit that is the principal's mailbox, not
	// the delegate's (a deliberate v1 default). Filing the copy and consuming the
	// source draft are best-effort follow-up, a failure here must not re-fail a
	// message that has already gone out (which would make the client resend it).
	_, _ = obj.store.AppendMessage(int64(mapi.PrivateFIDSentItems), raw, time.Now(), int64(objectstore.FlagSeen))
	_ = obj.store.DeleteObject(nm.savedID)
	nm.saved = false // the saved message is gone; a re-submit must not re-send

	out.Uint8(ropSubmitMessage)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	return true
}

// authorizeSubmit resolves the identities the outgoing copy is stamped with and
// checks the submit is allowed at all. ok=false means the response was already
// written.
//
// Submitting is governed by send-on-behalf: an owner sends as itself, a delegate
// only when designated on the mailbox's delegate list (folder rights alone do
// not confer it). The message must also be persisted (its assigned id is
// non-zero) and the session must have an MTA bridge wired, which a read-only
// session does not.
func (s *Session) authorizeSubmit(out *ext.Push, obj *object, ropID, hindex uint8) (representing, sender string, ok bool) {
	representing, sender, allowed, err := s.delegateSendIdentity(obj.store)
	if err != nil {
		writeErr(out, ropID, hindex, ecError)
		return "", "", false
	}
	if !allowed {
		writeErr(out, ropID, hindex, ecAccessDenied)
		return "", "", false
	}
	nm := obj.newMsg
	if !nm.saved || nm.savedID == 0 || s.accounts == nil {
		writeErr(out, ropID, hindex, ecNotSupported)
		return "", "", false
	}
	return representing, sender, true
}

// noRecipientOrError maps a delivery failure to its ROP return code: a message
// with nowhere to go reads as not-found, anything else as a generic error.
func noRecipientOrError(err error) uint32 {
	if errors.Is(err, errNoRecipient) {
		return ecNotFound
	}
	return ecError
}

// errNoRecipient is returned by deliverComposed when a saved composed message
// carries no routable SMTP recipient.
var errNoRecipient = errors.New("rop: no routable recipient")

// deliverComposed exports a saved composed message through oxcmail and hands it to
// the MTA bridge, the export+deliver core shared by RopSubmitMessage and
// RopTransportSend, the single proven outbound path the oxcmail.Export invariant
// protects. It splits the recipient bags (the delivery list takes every resolvable
// SMTP address To+Cc+Bcc, while the exported wire copy carries only To+Cc bags,
// oxcmail.Export writes a Bcc header for any RecipBcc bag, so leaving Bcc in the
// wire copy would disclose blind recipients to the To/Cc readers), stamps the
// representing and sender identities the caller resolved (representing is the From; a
// non-empty sender adds the on-behalf Sender), and returns the delivered raw bytes. It
// reports errNoRecipient when nothing is routable; the caller maps that (and any
// export/deliver fault) to its own ROP error code. The caller has already verified
// nm.saved, nm.savedID, and s.accounts.
func (s *Session) deliverComposed(nm *newMessageState, representing, sender string) ([]byte, error) {
	var recipients []string
	wire := make([]mapi.PropertyValues, 0, len(nm.recipients))
	for _, bag := range nm.recipients {
		if addr := recipientSMTP(bag); addr != "" {
			recipients = append(recipients, addr)
		}
		if rt, _ := bag.Get(mapi.PrRecipientType); rt != int32(mapi.RecipBcc) {
			wire = append(wire, bag)
		}
	}
	if len(recipients) == 0 {
		return nil, errNoRecipient
	}
	// Stamp the representing/sender identities + submit time: Export derives From from
	// the representing identity, so an unstamped message ships From-less and is
	// rejected downstream. Copy the bag first so the in-memory draft is untouched.
	props := append(mapi.PropertyValues(nil), nm.props...)
	// On an owner submit the client may legitimately name one of the owner's own
	// aliases as From; resolve that identity set so a foreign representing address
	// is overwritten rather than trusted.
	var ownerIDs []string
	if sender == "" {
		ownerIDs = s.ownerIdentities()
	}
	stampSubmitIdentity(&props, representing, sender, ownerIDs)
	oxcmail.EnsureMessageID(&props)

	raw, err := oxcmail.Export(&oxcmail.Message{Props: props, Recipients: wire}, oxcmail.Options{})
	if err != nil {
		return nil, err
	}
	if _, err := mta.DeliverAndRelay(s.accounts, s.spool, s.owner, recipients, raw, time.Now()); err != nil {
		return nil, err
	}
	return raw, nil
}

// recipientSMTP extracts a routable SMTP address from a recipient bag: the
// explicit PR_SMTP_ADDRESS if present, else PR_EMAIL_ADDRESS when the address
// type is SMTP. X500/EX recipients (resolved through NSPI) carry no SMTP address
// and yield "", v1 cannot route them.
func recipientSMTP(bag mapi.PropertyValues) string {
	if v, ok := bag.Get(mapi.PrSmtpAddress); ok {
		if s, _ := v.(string); s != "" {
			return s
		}
	}
	if v, ok := bag.Get(mapi.PrAddrType); ok {
		if at, _ := v.(string); strings.EqualFold(at, "SMTP") {
			if e, ok := bag.Get(mapi.PrEmailAddress); ok {
				if s, _ := e.(string); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

// stampSubmitIdentity fixes the representing/sender identities and submit time on a
// message about to be exported. An owner send (sender == "") keeps the client's
// representing address only when it is one of the owner's own identities (ownerIDs,
// the primary plus aliases); an unset, empty, or foreign address is overwritten with
// the owner's own address, so a client cannot put another user in the From. A delegate
// send-on-behalf (sender != "") FORCES both identities, overwriting whatever the client
// supplied: the representing identity is the mailbox owner (the From) and the sender is
// the delegate (the Sender). Export emits a Sender header whenever the two differ,
// producing the "<delegate> on behalf of <owner>" form.
func stampSubmitIdentity(props *mapi.PropertyValues, representing, sender string, ownerIDs []string) {
	if sender == "" {
		if representing != "" && !ownerMayRepresent(props, ownerIDs) {
			setRepresenting(props, representing)
		}
	} else {
		setRepresenting(props, representing)
		setSender(props, sender)
	}
	if _, ok := props.Get(mapi.PrClientSubmitTime); !ok {
		props.Set(mapi.PrClientSubmitTime, mapi.UnixToNTTime(time.Now()))
	}
}

// setRepresenting and setSender write the SMTP-address trio Export reads to format the
// From (representing) and Sender (sender) headers.
func setRepresenting(props *mapi.PropertyValues, addr string) {
	props.Set(mapi.PrSentRepresentingSmtpAddress, addr)
	props.Set(mapi.PrSentRepresentingEmailAddress, addr)
	props.Set(mapi.PrSentRepresentingAddrType, "SMTP")
}

func setSender(props *mapi.PropertyValues, addr string) {
	props.Set(mapi.PrSenderSmtpAddress, addr)
	props.Set(mapi.PrSenderEmailAddress, addr)
	props.Set(mapi.PrSenderAddrType, "SMTP")
}

// ownerMayRepresent reports whether the message's current representing address is
// one the owner is entitled to name in From: a non-empty PR_SENT_REPRESENTING_SMTP_
// ADDRESS matching one of ownerIDs (the owner's primary plus aliases, case-insensitive).
// An unset, empty, or foreign address returns false so the caller overwrites it with
// the owner's own address.
func ownerMayRepresent(props *mapi.PropertyValues, ownerIDs []string) bool {
	v, ok := props.Get(mapi.PrSentRepresentingSmtpAddress)
	if !ok {
		return false
	}
	cur, _ := v.(string)
	if cur == "" {
		return false
	}
	for _, id := range ownerIDs {
		if strings.EqualFold(strings.TrimSpace(id), strings.TrimSpace(cur)) {
			return true
		}
	}
	return false
}

// scanUploadedMessage scans the attachment content of a message a client just
// uploaded through FastTransfer, and removes the message when it matched. It
// reports whether the message was refused. A store error while removing it is
// recorded rather than swallowed: the message would otherwise stay readable.
func (s *Session) scanUploadedMessage(store *objectstore.Store, messageID int64) bool {
	msg, err := store.OpenMessage(messageID)
	if err != nil {
		return false
	}
	for _, att := range msg.Attachments {
		if !s.scanAttachmentContent(att.Props) {
			continue
		}
		if derr := store.DeleteObject(messageID); derr != nil {
			s.logger.Emit(logging.Event{
				Level: logging.LevelError, Subsystem: logging.MAPI, Name: "ics.upload.virus.delete.fail",
				User:   s.effectiveCaller(store),
				Fields: logging.Fields{"mailbox": store.Dir(), "message": messageID},
				Err:    derr.Error(),
			})
		}
		return true
	}
	return false
}
