package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// buildCreateMessage builds a RopCreateMessage request (OutputHandleIndex, Cpid,
// FolderId, AssociatedFlag).
func buildCreateMessage(inIdx, outIdx uint8, folderEID uint64) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropCreateMessage)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint16(0) // Cpid
	b.Uint64(folderEID)
	b.Uint8(0) // AssociatedFlag (not FAI)
	return b.Bytes()
}

// buildSetProperties builds a RopSetProperties request carrying a TPROPVAL_ARRAY
// in the length-prefixed value region.
func buildSetProperties(inIdx uint8, props mapi.PropertyValues) []byte {
	body := ext.NewPush(ext.FlagUTF16)
	_ = body.PropertyValues(props)
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSetProperties)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint16(uint16(len(body.Bytes()))) // PropertyValueSize
	b.Raw(body.Bytes())
	return b.Bytes()
}

// buildSMTPRecipientRow builds one MODIFYRECIPIENT_ROW for a unicode SMTP
// recipient: the EMAIL + DISPLAY flag fields plus a single trailing
// PR_SMTP_ADDRESS column (the NONE-form PROPERTY_ROW).
func buildSMTPRecipientRow(rowID uint32, rcptType uint8, email, display string) []byte {
	row := ext.NewPush(ext.FlagUTF16)
	row.Uint16(recipientRowEmail | recipientRowDisplay | recipientRowUnicode | addrKindSMTP)
	row.Unicode(email)   // pmail_address (g_wstr)
	row.Unicode(display) // pdisplay_name (g_wstr)
	row.Uint16(1)        // RecipientColumnCount
	row.Uint8(propertyRowNone)
	_ = row.PropValue(mapi.PrSmtpAddress.Type(), email)
	rowBytes := row.Bytes()

	b := ext.NewPush(ext.FlagUTF16)
	b.Uint32(rowID)
	b.Uint8(rcptType)
	b.Uint16(uint16(len(rowBytes))) // RecipientRowSize
	b.Raw(rowBytes)
	return b.Bytes()
}

// buildModifyRecipients builds a RopModifyRecipients request over the given
// recipient columns and pre-encoded MODIFYRECIPIENT_ROWs.
func buildModifyRecipients(inIdx uint8, columns []mapi.PropTag, rows ...[]byte) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropModifyRecipients)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	_ = b.PropTags(columns)
	b.Uint16(uint16(len(rows))) // RowCount
	for _, r := range rows {
		b.Raw(r)
	}
	return b.Bytes()
}

// buildSaveChangesMessage builds a RopSaveChangesMessage request. respIdx is the
// common-header ResponseHandleIndex; msgIdx is the body InputHandleIndex that
// indexes the message object — deliberately distinct so the handle resolution
// is exercised.
func buildSaveChangesMessage(respIdx, msgIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSaveChangesMessage)
	b.Uint8(0) // LogonId
	b.Uint8(respIdx)
	b.Uint8(msgIdx) // ihindex2
	b.Uint8(0)      // SaveFlags
	return b.Bytes()
}

// TestModifyRecipientRowParse locks the byte layout of the MODIFYRECIPIENT_ROW /
// RECIPIENT_ROW parser in isolation: a unicode SMTP recipient with the EMAIL and
// DISPLAY flag fields and a trailing PR_SMTP_ADDRESS column must map to a bag
// carrying every well-known recipient property.
func TestModifyRecipientRowParse(t *testing.T) {
	columns := []mapi.PropTag{mapi.PrSmtpAddress}
	rowBytes := buildSMTPRecipientRow(7, mapi.RecipCc, "bob@hermex.test", "Bob")

	p := ext.NewPull(rowBytes, ext.FlagUTF16)
	bag, ok, err := pullModifyRecipientBag(p, columns)
	if err != nil {
		t.Fatalf("pullModifyRecipientBag: %v", err)
	}
	if !ok {
		t.Fatal("recipient row was skipped, want included")
	}
	if v, _ := bag.Get(mapi.PrRowid); v != int32(7) {
		t.Errorf("PrRowid = %v, want 7", v)
	}
	if v, _ := bag.Get(mapi.PrRecipientType); v != int32(mapi.RecipCc) {
		t.Errorf("PrRecipientType = %v, want %d", v, mapi.RecipCc)
	}
	if v, _ := bag.Get(mapi.PrEmailAddress); v != "bob@hermex.test" {
		t.Errorf("PrEmailAddress = %v, want bob@hermex.test", v)
	}
	if v, _ := bag.Get(mapi.PrDisplayName); v != "Bob" {
		t.Errorf("PrDisplayName = %v, want Bob", v)
	}
	if v, _ := bag.Get(mapi.PrAddrType); v != "SMTP" {
		t.Errorf("PrAddrType = %v, want SMTP", v)
	}
	if v, ok := bag.Get(mapi.PrSmtpAddress); !ok || v != "bob@hermex.test" {
		t.Errorf("PrSmtpAddress (trailing column) = %v present=%v, want bob@hermex.test", v, ok)
	}
	if v, _ := bag.Get(mapi.PrResponsibility); v != false {
		t.Errorf("PrResponsibility = %v, want false (flag unset)", v)
	}
	if p.Remaining() != 0 {
		t.Errorf("recipient row left %d bytes unconsumed", p.Remaining())
	}
}

// TestModifyRecipientRowRemoval confirms a zero-size row (the recipient-removal
// marker) is skipped under full-set replace semantics.
func TestModifyRecipientRowRemoval(t *testing.T) {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint32(3) // RowId
	b.Uint8(1)  // RecipientType
	b.Uint16(0) // RecipientRowSize == 0
	p := ext.NewPull(b.Bytes(), ext.FlagUTF16)
	_, ok, err := pullModifyRecipientBag(p, nil)
	if err != nil {
		t.Fatalf("pullModifyRecipientBag: %v", err)
	}
	if ok {
		t.Error("zero-size recipient row was included, want skipped")
	}
}

// TestCreateFillSaveRoundTrip drives the full ROP write sequence — CreateMessage,
// SetProperties, ModifyRecipients, SaveChangesMessage — then re-reads the saved
// message both through the ROP layer (by the EID the save returned) and directly
// from the store, proving the message and its recipient actually persisted.
func TestCreateFillSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir)
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	// CreateMessage off the logon: parent at slot 0, new message at slot 1.
	cm, h := sess.Dispatch(buildCreateMessage(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	p := ext.NewPull(cm, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropCreateMessage {
		t.Fatalf("CreateMessage RopId = %#x", id)
	}
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("CreateMessage ReturnValue = %#x", ec)
	}
	if hasID := mustU8(t, p, "hasMessageId"); hasID != 0 {
		t.Errorf("CreateMessage HasMessageId = %d, want 0 (id assigned at save)", hasID)
	}
	msgH := h[1]
	if obj := sess.get(msgH); obj == nil || obj.kind != kindNewMessage {
		t.Fatalf("new-message object wrong: %+v", obj)
	}

	// SetProperties: subject onto the open message (slot 0 in this call).
	sp, _ := sess.Dispatch(
		buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "WRITEMSG"}}),
		[]uint32{msgH})
	p = ext.NewPull(sp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSetProperties {
		t.Fatalf("SetProperties RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("SetProperties ReturnValue = %#x", ec)
	}
	if pc := mustU16(t, p, "problemCount"); pc != 0 {
		t.Errorf("SetProperties PropertyProblemCount = %d, want 0", pc)
	}

	// ModifyRecipients: one SMTP To recipient.
	row := buildSMTPRecipientRow(0, mapi.RecipTo, "alice@hermex.test", "Alice")
	mr, _ := sess.Dispatch(buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, row), []uint32{msgH})
	p = ext.NewPull(mr, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropModifyRecipients {
		t.Fatalf("ModifyRecipients RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("ModifyRecipients ReturnValue = %#x", ec)
	}

	// SaveChangesMessage: the message lives at slot 1 (ihindex2), while the
	// common-header ResponseHandleIndex points at slot 0 (the logon). Resolving
	// the message at the header handle instead of ihindex2 would fail here.
	sc, _ := sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})
	p = ext.NewPull(sc, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSaveChangesMessage {
		t.Fatalf("SaveChangesMessage RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("SaveChangesMessage ReturnValue = %#x (message must resolve at ihindex2)", ec)
	}
	if ih2 := mustU8(t, p, "ihindex2"); ih2 != 1 {
		t.Errorf("SaveChangesMessage echoed ihindex2 = %d, want 1", ih2)
	}
	savedEID, err := p.Uint64()
	if err != nil {
		t.Fatalf("SaveChangesMessage MessageId: %v", err)
	}
	if savedEID == 0 {
		t.Fatal("SaveChangesMessage returned a zero MessageId")
	}
	savedID := int64(mapi.EID(savedEID).GCValue())

	// Black-box: re-open by the returned EID through the ROP layer and read the
	// subject back, proving the EID round-trips and the property persisted.
	om, h := sess.Dispatch(buildOpenMessage(0, 1, inboxEID, savedEID), []uint32{logonH, 0xFFFFFFFF})
	p = ext.NewPull(om, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("OpenMessage(saved EID) ReturnValue = %#x", ec)
	}
	reopenedH := h[1]
	cols := []mapi.PropTag{mapi.PrSubject}
	gps, _ := sess.Dispatch(buildGetProps(ropGetPropertiesSpecific, 0, cols), []uint32{reopenedH})
	p = ext.NewPull(gps, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("GetPropertiesSpecific(saved) ReturnValue = %#x", ec)
	}
	rrow := decodeRow(t, p, cols)
	if subj, _ := rrow.Get(mapi.PrSubject); subj != "WRITEMSG" {
		t.Errorf("re-read subject = %v, want WRITEMSG", subj)
	}

	// White-box: open the store directly to confirm the recipient persisted —
	// the ROP OpenMessage response does not surface recipients (v1), so this is
	// the only way to verify the recipient survived the write.
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	saved, err := st.OpenMessage(savedID)
	if err != nil {
		t.Fatalf("store.OpenMessage(%d): %v", savedID, err)
	}
	if len(saved.Recipients) != 1 {
		t.Fatalf("saved message has %d recipients, want 1", len(saved.Recipients))
	}
	if v, _ := saved.Recipients[0].Get(mapi.PrEmailAddress); v != "alice@hermex.test" {
		t.Errorf("recipient PrEmailAddress = %v, want alice@hermex.test", v)
	}
	if v, _ := saved.Recipients[0].Get(mapi.PrRecipientType); v != int32(mapi.RecipTo) {
		t.Errorf("recipient PrRecipientType = %v, want %d", v, mapi.RecipTo)
	}
}
