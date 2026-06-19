package rop

import (
	"errors"
	"strings"
	"time"

	"hermex/internal/ext"
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
// type, size) could not be read — an unrecoverable desync that ends the batch,
// unlike a malformed row body, which is skipped (the row was size-bounded).
var errRecipientFraming = errors.New("rop: malformed recipient row framing")

// ropCreateMessage handles RopCreateMessage ([MS-OXCMSG] 2.2.3.1): it opens an
// in-memory message under the output handle, to be filled by SetProperties /
// ModifyRecipients and persisted by SaveChangesMessage. The response carries no
// message id (HasMessageId 0) — the id is allocated when the message is saved.
func (s *Session) ropCreateMessage(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8()    // OutputHandleIndex
	_, e2 := p.Uint16()         // CodePageId
	folderEID, e3 := p.Uint64() // FolderId
	associated, e4 := p.Uint8() // AssociatedFlag
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return false
	}
	parent := s.get(handleAt(handles, hindex))
	if parent == nil || parent.store == nil {
		writeErr(out, ropCreateMessage, ohindex, ecError)
		return true
	}
	fid := int64(mapi.EID(folderEID).GCValue())
	exists, err := parent.store.FolderExists(fid)
	if err != nil {
		writeErr(out, ropCreateMessage, ohindex, ecError)
		return true
	}
	if !exists {
		writeErr(out, ropCreateMessage, ohindex, ecNotFound)
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

// ropSetProperties handles RopSetProperties ([MS-OXCPRPT] 2.2.2.5): it merges
// the request's TPROPVAL_ARRAY into the open message's property bag. The values
// occupy a length-bounded region, read from an isolated slice so trailing bytes
// in that region cannot be over-read. It supports both a message being composed
// (kindNewMessage) and an existing message opened for edit (kindMessage), whose
// changes are buffered in pendingProps and flushed by SaveChangesMessage —
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
	switch obj.kind {
	case kindNewMessage:
		for _, tv := range propvals {
			obj.newMsg.props.Set(tv.Tag, tv.Value)
		}
	case kindMessage:
		for _, tv := range propvals {
			obj.pendingProps.Set(tv.Tag, tv.Value)
			// A set supersedes a buffered delete for the same tag: drop the tag from
			// pendingDeletes so SaveChangesMessage does not delete the row it just
			// inserted (delete-then-set within one edit session). This is the mirror
			// of deleteProperties dropping a buffered set.
			obj.pendingDeletes = dropDeleteTag(obj.pendingDeletes, tv.Tag)
		}
	case kindAttachWrite:
		for _, tv := range propvals {
			obj.attachW.pending.Set(tv.Tag, tv.Value)
			obj.attachW.pendingDeletes = dropDeleteTag(obj.attachW.pendingDeletes, tv.Tag)
		}
	case kindEmbedded:
		// A composed embedded message buffers its edits in memory; they are exported
		// into the parent attachment when SaveChangesMessage runs.
		for _, tv := range propvals {
			obj.embedded.msg.Props.Set(tv.Tag, tv.Value)
		}
	default:
		writeErr(out, ropSetProperties, hindex, ecError)
		return true
	}

	out.Uint8(ropSetProperties)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(0) // PropertyProblemCount
	return true
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
			return false // framing desync — the batch can no longer be located
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
	ihindex2, e1 := p.Uint8() // InputHandleIndex (indexes the message object)
	_, e2 := p.Uint8()        // SaveFlags
	if e1 != nil || e2 != nil {
		return false
	}
	obj := s.get(handleAt(handles, ihindex2))
	if obj == nil || obj.store == nil {
		writeErr(out, ropSaveChangesMessage, hindex, ecError)
		return true
	}
	// An ICS-imported message commits its FastTransfer-uploaded body here, the same
	// point a composed message is saved ([MS-OXCFXICS] 3.3.5.6).
	if obj.kind == kindUploadMessage {
		if obj.uploadMsg == nil {
			writeErr(out, ropSaveChangesMessage, hindex, ecError)
			return true
		}
		mid, err := obj.uploadMsg.Commit()
		if err != nil {
			writeErr(out, ropSaveChangesMessage, hindex, ecError)
			return true
		}
		out.Uint8(ropSaveChangesMessage)
		out.Uint8(hindex)
		out.Uint32(ecSuccess)
		out.Uint8(ihindex2)
		out.Uint64(uint64(mapi.MakeEIDEx(1, mid)))
		return true
	}
	// An existing message opened for edit flushes its buffered property changes
	// in place, reallocating the change number so ICS observes the edit. A pending
	// property change or a touched flag (set when an attachment was added or
	// deleted) bumps the change number; with neither, the save is a no-op success
	// (no spurious bump), matching the reference's untouched-message early-out. An
	// attachment-only change carries no pending properties, so ModifyMessageProperties
	// runs with an empty bag and advances only the change number.
	if obj.kind == kindMessage {
		if len(obj.pendingProps) > 0 || len(obj.pendingDeletes) > 0 || obj.touched {
			if err := obj.store.ModifyMessageProperties(obj.messageID, obj.pendingProps, obj.pendingDeletes...); err != nil {
				writeErr(out, ropSaveChangesMessage, hindex, ecError)
				return true
			}
			obj.pendingProps = nil
			obj.pendingDeletes = nil
			obj.touched = false
		}
		out.Uint8(ropSaveChangesMessage)
		out.Uint8(hindex)
		out.Uint32(ecSuccess)
		out.Uint8(ihindex2)
		out.Uint64(uint64(mapi.MakeEIDEx(1, uint64(obj.messageID))))
		return true
	}
	// A composed embedded message is persisted by exporting it back into its parent
	// attachment: the export bytes, method, and MIME tag are staged into the
	// attachment's pending bag, which the client's SaveChangesAttachment then writes
	// through the ordinary attachment path. A read-only embedded message (opened over
	// an existing attachment) has no write-back target and cannot be saved.
	if obj.kind == kindEmbedded {
		emb := obj.embedded
		if emb == nil || emb.writeback == nil {
			writeErr(out, ropSaveChangesMessage, hindex, ecNotSupported)
			return true
		}
		raw, err := oxcmail.Export(emb.msg, oxcmail.Options{})
		if err != nil {
			writeErr(out, ropSaveChangesMessage, hindex, ecError)
			return true
		}
		emb.writeback.pending.Set(mapi.PrAttachMethod, int32(mapi.AttachEmbeddedMsg))
		emb.writeback.pending.Set(mapi.PrAttachMimeTag, "message/rfc822")
		emb.writeback.pending.Set(mapi.PrAttachDataBin, raw)

		out.Uint8(ropSaveChangesMessage)
		out.Uint8(hindex)
		out.Uint32(ecSuccess)
		out.Uint8(ihindex2)
		out.Uint64(uint64(mapi.MakeEIDEx(1, uint64(handleAt(handles, ihindex2)))))
		return true
	}
	if obj.kind != kindNewMessage {
		writeErr(out, ropSaveChangesMessage, hindex, ecError)
		return true
	}
	nm := obj.newMsg
	var id int64
	var err error
	if nm.saved {
		// A composed message re-saved after its first persist is an in-place edit:
		// reallocate the change number through the same path an opened message
		// uses, rather than a pure upsert (which would leave the message looking
		// unchanged to ICS).
		err = obj.store.ModifyMessageProperties(nm.savedID, nm.props)
		id = nm.savedID
	} else {
		// The attachments staged during compose are written together with the message.
		atts := make([]oxcmail.Attachment, len(nm.attachments))
		for i, a := range nm.attachments {
			atts[i] = oxcmail.Attachment{Props: a.props}
		}
		id, err = obj.store.CreateMessage(nm.folderID, &oxcmail.Message{
			Props:       nm.props,
			Recipients:  nm.recipients,
			Attachments: atts,
		})
	}
	if err != nil {
		writeErr(out, ropSaveChangesMessage, hindex, ecError)
		return true
	}
	nm.saved = true
	nm.savedID = id

	out.Uint8(ropSaveChangesMessage)
	out.Uint8(hindex) // ResponseHandleIndex (echoed in the header)
	out.Uint32(ecSuccess)
	out.Uint8(ihindex2) // InputHandleIndex (echoed in the body)
	out.Uint64(uint64(mapi.MakeEIDEx(1, uint64(id))))
	return true
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
	unicode := flags&recipientRowUnicode != 0
	addrKind := flags & 0x0007

	readStr := func() (string, error) {
		if unicode {
			return p.Unicode()
		}
		return p.String8()
	}

	var x500dn, addrType, mailAddr, displayName, transmitName string
	var haveX500, haveAddrType, haveMail, haveDisplay, haveTransmit bool

	switch addrKind {
	case addrKindX500DN:
		if _, err = p.Uint8(); err != nil { // PrefixUsed
			return nil, false
		}
		if _, err = p.Uint8(); err != nil { // DisplayType
			return nil, false
		}
		if x500dn, err = p.String8(); err != nil { // X500DN (always ASCII)
			return nil, false
		}
		haveX500 = true
	case addrKindDList1, addrKindDList2:
		if _, err = p.Bin(); err != nil { // EntryId
			return nil, false
		}
		if _, err = p.Bin(); err != nil { // SearchKey
			return nil, false
		}
	}
	if addrKind == addrKindNoType && flags&recipientRowOutOfStandard != 0 {
		if addrType, err = p.String8(); err != nil { // AddressType (always ASCII)
			return nil, false
		}
		haveAddrType = true
	}
	if flags&recipientRowEmail != 0 {
		if mailAddr, err = readStr(); err != nil {
			return nil, false
		}
		haveMail = true
	}
	if flags&recipientRowDisplay != 0 {
		if displayName, err = readStr(); err != nil {
			return nil, false
		}
		haveDisplay = true
	}
	if flags&recipientRowSimple != 0 {
		if _, err = readStr(); err != nil { // SimpleDisplayName (v1 ignores)
			return nil, false
		}
	}
	if flags&recipientRowTransmittable != 0 {
		if transmitName, err = readStr(); err != nil {
			return nil, false
		}
		haveTransmit = true
	}

	bag := mapi.PropertyValues{}
	bag.Set(mapi.PrResponsibility, flags&recipientRowResponsible != 0)
	bag.Set(mapi.PrSendRichInfo, flags&recipientRowNonRich != 0)
	if haveTransmit {
		bag.Set(mapi.PrTransmitableDisplayName, transmitName)
	}
	if haveDisplay {
		bag.Set(mapi.PrDisplayName, displayName)
	}
	if haveMail {
		bag.Set(mapi.PrEmailAddress, mailAddr)
	}
	switch addrKind {
	case addrKindNoType:
		if haveAddrType {
			bag.Set(mapi.PrAddrType, addrType)
		}
	case addrKindX500DN:
		if !haveX500 {
			return nil, false
		}
		bag.Set(mapi.PrAddrType, "EX")
		bag.Set(mapi.PrEmailAddress, x500dn)
	case addrKindSMTP:
		bag.Set(mapi.PrAddrType, "SMTP")
	default:
		return nil, false // MSMAIL / FAX / personal distribution list — unsupported in v1
	}

	// Trailing PROPERTY_ROW over the first RecipientColumnCount columns; its
	// values (e.g. PR_SMTP_ADDRESS) are merged after the flag-driven fields.
	count, err := p.Uint16()
	if err != nil {
		return nil, false
	}
	if int(count) > len(columns) {
		return nil, false
	}
	if err := pullPropertyRow(p, columns[:count], &bag); err != nil {
		return nil, false
	}
	return bag, true
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
		for _, col := range columns {
			v, err := p.PropValue(col.Type())
			if err != nil {
				return err
			}
			bag.Set(col, v)
		}
	case propertyRowFlagged:
		for _, col := range columns {
			marker, err := p.Uint8()
			if err != nil {
				return err
			}
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
	if _, err := p.Uint8(); err != nil { // SubmitFlags (PreProcess/NeedsSpooler — v1 ignores)
		return false
	}
	obj := s.get(handleAt(handles, hindex))
	if obj == nil || obj.kind != kindNewMessage || obj.newMsg == nil {
		writeErr(out, ropSubmitMessage, hindex, ecNotFound)
		return true
	}
	nm := obj.newMsg
	// The message must be persisted (the reference checks the assigned id is
	// non-zero) and the session must have an MTA bridge wired (a read-only
	// session carries none).
	if !nm.saved || nm.savedID == 0 || s.accounts == nil {
		writeErr(out, ropSubmitMessage, hindex, ecNotSupported)
		return true
	}

	raw, err := s.deliverComposed(nm)
	if err != nil {
		if errors.Is(err, errNoRecipient) {
			writeErr(out, ropSubmitMessage, hindex, ecNotFound) // no routable recipient
		} else {
			writeErr(out, ropSubmitMessage, hindex, ecError)
		}
		return true
	}
	// Delivery has succeeded. Filing the Sent Items copy and consuming the source
	// draft are best-effort follow-up — a failure here must not re-fail a message
	// that has already gone out (which would make the client resend it).
	_, _ = obj.store.AppendMessage(int64(mapi.PrivateFIDSentItems), raw, time.Now(), int64(objectstore.FlagSeen))
	_ = obj.store.DeleteObject(nm.savedID)
	nm.saved = false // the saved message is gone; a re-submit must not re-send

	out.Uint8(ropSubmitMessage)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	return true
}

// errNoRecipient is returned by deliverComposed when a saved composed message
// carries no routable SMTP recipient.
var errNoRecipient = errors.New("rop: no routable recipient")

// deliverComposed exports a saved composed message through oxcmail and hands it to
// the MTA bridge — the export+deliver core shared by RopSubmitMessage and
// RopTransportSend, the single proven outbound path the oxcmail.Export invariant
// protects. It splits the recipient bags (the delivery list takes every resolvable
// SMTP address To+Cc+Bcc, while the exported wire copy carries only To+Cc bags —
// oxcmail.Export writes a Bcc header for any RecipBcc bag, so leaving Bcc in the
// wire copy would disclose blind recipients to the To/Cc readers), stamps the
// sender-representing identity the wire copy needs, and returns the delivered raw
// bytes. It reports errNoRecipient when nothing is routable; the caller maps that
// (and any export/deliver fault) to its own ROP error code. The caller has already
// verified nm.saved, nm.savedID, and s.accounts.
func (s *Session) deliverComposed(nm *newMessageState) ([]byte, error) {
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
	// Stamp the sender-representing identity + submit time when the client left them
	// unset (the reference rectifies the message at send the same way): Export
	// derives From from the representing identity, so an unstamped message ships
	// From-less and is rejected downstream. Copy the bag first so the in-memory
	// draft is untouched.
	props := append(mapi.PropertyValues(nil), nm.props...)
	stampSubmitIdentity(&props, s.owner)

	raw, err := oxcmail.Export(&oxcmail.Message{Props: props, Recipients: wire}, oxcmail.Options{GenerateMessageID: true})
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
// and yield "" — v1 cannot route them.
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

// stampSubmitIdentity fills the sender-representing identity and submit time on a
// message about to be exported, when the client did not set them. owner is the
// session owner's SMTP address.
func stampSubmitIdentity(props *mapi.PropertyValues, owner string) {
	if v, ok := props.Get(mapi.PrSentRepresentingSmtpAddress); owner != "" && (!ok || v == "") {
		props.Set(mapi.PrSentRepresentingSmtpAddress, owner)
		props.Set(mapi.PrSentRepresentingEmailAddress, owner)
		props.Set(mapi.PrSentRepresentingAddrType, "SMTP")
	}
	if _, ok := props.Get(mapi.PrClientSubmitTime); !ok {
		props.Set(mapi.PrClientSubmitTime, mapi.UnixToNTTime(time.Now()))
	}
}
