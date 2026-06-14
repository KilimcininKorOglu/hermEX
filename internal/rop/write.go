package rop

import (
	"errors"

	"hermex/internal/ext"
	"hermex/internal/mapi"
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
	if associated != 0 {
		// Associated (FAI) messages are out of v1 scope; reject rather than
		// silently store a normal message the client believes is associated.
		writeErr(out, ropCreateMessage, ohindex, ecNotSupported)
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
	h := s.alloc(&object{
		kind:   kindNewMessage,
		store:  parent.store,
		newMsg: &newMessageState{folderID: fid, props: mapi.PropertyValues{}},
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
// in that region cannot be over-read. v1 supports it on a message being
// composed; it reports no property problems.
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
	if obj == nil || obj.kind != kindNewMessage {
		writeErr(out, ropSetProperties, hindex, ecError)
		return true
	}
	for _, tv := range propvals {
		obj.newMsg.props.Set(tv.Tag, tv.Value)
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
	for i := 0; i < int(count); i++ {
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
	if obj == nil || obj.kind != kindNewMessage || obj.store == nil {
		writeErr(out, ropSaveChangesMessage, hindex, ecError)
		return true
	}
	nm := obj.newMsg
	var id int64
	var err error
	if nm.saved {
		err = obj.store.SetMessageProperties(nm.savedID, nm.props)
		id = nm.savedID
	} else {
		id, err = obj.store.CreateMessage(nm.folderID, &oxcmail.Message{
			Props:      nm.props,
			Recipients: nm.recipients,
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
