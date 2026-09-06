package rop

import (
	"bytes"
	"testing"

	"hermex/internal/directory"
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
// indexes the message object, deliberately distinct so the handle resolution
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
	wantProp(t, bag, mapi.PrRowid, int32(7), "PrRowid")
	wantProp(t, bag, mapi.PrRecipientType, int32(mapi.RecipCc), "PrRecipientType")
	wantProp(t, bag, mapi.PrEmailAddress, "bob@hermex.test", "PrEmailAddress")
	wantProp(t, bag, mapi.PrDisplayName, "Bob", "PrDisplayName")
	wantProp(t, bag, mapi.PrAddrType, "SMTP", "PrAddrType")
	wantProp(t, bag, mapi.PrSmtpAddress, "bob@hermex.test", "PrSmtpAddress (trailing column)")
	wantProp(t, bag, mapi.PrResponsibility, false, "PrResponsibility (flag unset)")
	wantDrained(t, p, "the recipient row")
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

// TestCreateFillSaveRoundTrip drives the full ROP write sequence, CreateMessage,
// SetProperties, ModifyRecipients, SaveChangesMessage, then re-reads the saved
// message both through the ROP layer (by the EID the save returned) and directly
// from the store, proving the message and its recipient actually persisted.
func TestCreateFillSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	// CreateMessage off the logon: parent at slot 0, new message at slot 1.
	cm, h := sess.Dispatch(buildCreateMessage(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	p := ropOK(t, cm, ropCreateMessage, "CreateMessage")
	wantU8(t, p, "CreateMessage HasMessageId (id assigned at save)", 0)
	msgH := h[1]
	if obj := sess.get(msgH); obj == nil || obj.kind != kindNewMessage {
		t.Fatalf("new-message object wrong: %+v", obj)
	}

	// SetProperties: subject onto the open message (slot 0 in this call).
	sp, _ := sess.Dispatch(
		buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "WRITEMSG"}}),
		[]uint32{msgH})
	p = ropOK(t, sp, ropSetProperties, "SetProperties")
	wantU16(t, p, "SetProperties PropertyProblemCount", 0)

	// ModifyRecipients: one SMTP To recipient.
	row := buildSMTPRecipientRow(0, mapi.RecipTo, "alice@hermex.test", "Alice")
	mr, _ := sess.Dispatch(buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, row), []uint32{msgH})
	ropOK(t, mr, ropModifyRecipients, "ModifyRecipients")

	// SaveChangesMessage: the message lives at slot 1 (ihindex2), while the
	// common-header ResponseHandleIndex points at slot 0 (the logon). Resolving
	// the message at the header handle instead of ihindex2 would fail here.
	sc, _ := sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})
	p = ropOK(t, sc, ropSaveChangesMessage, "SaveChangesMessage (message must resolve at ihindex2)")
	wantU8(t, p, "SaveChangesMessage ihindex2", 1)
	savedEID := mustU64(t, p, "SaveChangesMessage MessageId")
	if savedEID == 0 {
		t.Fatal("SaveChangesMessage returned a zero MessageId")
	}
	savedID := int64(mapi.EID(savedEID).GCValue())

	// Black-box: re-open by the returned EID through the ROP layer and read the
	// subject back, proving the EID round-trips and the property persisted.
	om, h := sess.Dispatch(buildOpenMessage(0, 1, inboxEID, savedEID), []uint32{logonH, 0xFFFFFFFF})
	ropOK(t, om, ropOpenMessage, "OpenMessage(saved EID)")
	reopenedH := h[1]
	cols := []mapi.PropTag{mapi.PrSubject}
	gps, _ := sess.Dispatch(buildGetProps(ropGetPropertiesSpecific, 0, cols), []uint32{reopenedH})
	p = ropOK(t, gps, ropGetPropertiesSpecific, "GetPropertiesSpecific(saved)")
	rrow := decodeRow(t, p, cols)
	if subj, _ := rrow.Get(mapi.PrSubject); subj != "WRITEMSG" {
		t.Errorf("re-read subject = %v, want WRITEMSG", subj)
	}

	assertSavedRecipient(t, dir, savedID)
}

// assertSavedRecipient opens the store directly to confirm the recipient
// persisted. The ROP OpenMessage response does not surface recipients in v1, so
// this white-box read is the only way to verify the write.
func assertSavedRecipient(t *testing.T, dir string, savedID int64) {
	t.Helper()
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

// buildSubmitMessage builds a RopSubmitMessage request (SubmitFlags only).
func buildSubmitMessage(inIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSubmitMessage)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(0) // SubmitFlags
	return b.Bytes()
}

// TestCreateFillSaveSubmitDelivers drives the full compose-and-send ROP sequence
// through to actual delivery: a message addressed To alice and Bcc carol is
// created in Drafts, filled, saved, and submitted. It then confirms the message
// reached both recipients' mailboxes, that the wire copy carries a From line for
// the session owner but never discloses the Bcc recipient, that a Sent Items copy
// was filed, and that the source draft was consumed.
func TestCreateFillSaveSubmitDelivers(t *testing.T) {
	ownerDir, aliceDir, carolDir := t.TempDir(), t.TempDir(), t.TempDir()
	accounts := directory.StaticAccounts{
		"owner@hermex.test": {MailboxPath: ownerDir},
		"alice@hermex.test": {MailboxPath: aliceDir},
		"carol@hermex.test": {MailboxPath: carolDir},
	}
	draftsEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDraft))

	sess := NewSession(ownerDir, accounts, "owner@hermex.test")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	// CreateMessage in Drafts (the source folder); new message at slot 1.
	_, h = sess.Dispatch(buildCreateMessage(0, 1, draftsEID), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]

	// SetProperties: subject + body.
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "SUBMITMSG"},
		{Tag: mapi.PrBody, Value: "hello from the rop submit path"},
	}), []uint32{msgH})

	// ModifyRecipients: alice as To, carol as Bcc.
	toRow := buildSMTPRecipientRow(0, mapi.RecipTo, "alice@hermex.test", "Alice")
	bccRow := buildSMTPRecipientRow(1, mapi.RecipBcc, "carol@hermex.test", "Carol")
	sess.Dispatch(buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, toRow, bccRow), []uint32{msgH})

	// SaveChangesMessage: message at slot 1 (ihindex2).
	sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})

	// SubmitMessage.
	sub, _ := sess.Dispatch(buildSubmitMessage(0), []uint32{msgH})
	ropOK(t, sub, ropSubmitMessage, "SubmitMessage")

	// alice (To) received it: the wire copy must carry a From line for the owner
	// and the subject, and must never disclose the Bcc recipient.
	aliceRaw := firstInboxRaw(t, aliceDir)
	if !hasFromOwner(aliceRaw, "owner@hermex.test") {
		t.Errorf("delivered message has no From line for the owner:\n%s", aliceRaw)
	}
	if !bytes.Contains(aliceRaw, []byte("SUBMITMSG")) {
		t.Errorf("delivered message missing subject SUBMITMSG:\n%s", aliceRaw)
	}
	if bytes.Contains(aliceRaw, []byte("carol")) || bytes.Contains(bytes.ToLower(aliceRaw), []byte("bcc:")) {
		t.Errorf("Bcc recipient leaked onto the wire copy:\n%s", aliceRaw)
	}

	// carol (Bcc) was delivered to as well, blind, but delivered.
	if n := inboxCount(t, carolDir); n != 1 {
		t.Errorf("carol (Bcc) inbox = %d messages, want 1", n)
	}

	assertSubmitConsumedDraft(t, ownerDir)
}

// assertSubmitConsumedDraft checks the sender's own mailbox after a submit: a
// Sent Items copy exists and the source draft is gone.
func assertSubmitConsumedDraft(t *testing.T, ownerDir string) {
	t.Helper()
	st, err := objectstore.Open(ownerDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	wantFolderCount(t, st, mapi.PrivateFIDSentItems, 1, "owner Sent Items")
	wantFolderCount(t, st, mapi.PrivateFIDDraft, 0, "source draft (consumed on submit)")
}

// wantFolderCount asserts how many messages a folder holds.
func wantFolderCount(t *testing.T, st *objectstore.Store, fid uint64, want int, label string) {
	t.Helper()
	msgs, err := st.ListMessages(int64(fid))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != want {
		t.Errorf("%s = %d messages, want %d", label, len(msgs), want)
	}
}

// TestSubmitOverwritesForgedRepresenting proves an owner submit cannot ship a
// From header naming another local user: the client sets PR_SENT_REPRESENTING_SMTP_
// ADDRESS to a co-worker's address before submitting, but the delivered wire copy
// carries the owner's own From, never the forged one. Without the owner-identity
// gate the spoofed representing address would flow straight into the From header and
// get DKIM-signed as an internal impersonation.
func TestSubmitOverwritesForgedRepresenting(t *testing.T) {
	ownerDir, aliceDir, victimDir := t.TempDir(), t.TempDir(), t.TempDir()
	accounts := directory.StaticAccounts{
		"owner@hermex.test":  {MailboxPath: ownerDir},
		"alice@hermex.test":  {MailboxPath: aliceDir},
		"victim@hermex.test": {MailboxPath: victimDir},
	}
	draftsEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDraft))

	sess := NewSession(ownerDir, accounts, "owner@hermex.test")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	_, h = sess.Dispatch(buildCreateMessage(0, 1, draftsEID), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]

	// The client forges the representing address to a co-worker before submitting.
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "FORGED"},
		{Tag: mapi.PrBody, Value: "spoof attempt"},
		{Tag: mapi.PrSentRepresentingSmtpAddress, Value: "victim@hermex.test"},
		{Tag: mapi.PrSentRepresentingEmailAddress, Value: "victim@hermex.test"},
		{Tag: mapi.PrSentRepresentingAddrType, Value: "SMTP"},
	}), []uint32{msgH})

	toRow := buildSMTPRecipientRow(0, mapi.RecipTo, "alice@hermex.test", "Alice")
	sess.Dispatch(buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, toRow), []uint32{msgH})
	sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})

	sub, _ := sess.Dispatch(buildSubmitMessage(0), []uint32{msgH})
	p := ext.NewPull(sub, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSubmitMessage {
		t.Fatalf("SubmitMessage RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("SubmitMessage ReturnValue = %#x", ec)
	}

	aliceRaw := firstInboxRaw(t, aliceDir)
	if !hasFromOwner(aliceRaw, "owner@hermex.test") {
		t.Errorf("delivered From is not the owner; the forged representing address was trusted:\n%s", aliceRaw)
	}
	if bytes.Contains(bytes.ToLower(aliceRaw), []byte("victim@hermex.test")) {
		t.Errorf("forged representing address leaked into the delivered message:\n%s", aliceRaw)
	}
}

// firstInboxRaw opens a mailbox and returns the re-synthesized raw of the single
// message in its inbox, failing if the count is not exactly one.
func firstInboxRaw(t *testing.T, dir string) []byte {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("inbox at %s = %d messages, want 1", dir, len(msgs))
	}
	raw, err := st.GetMessageRaw(int64(mapi.PrivateFIDInbox), msgs[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// inboxCount returns the number of messages in a mailbox's inbox.
func inboxCount(t *testing.T, dir string) int {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

// hasFromOwner reports whether the message header block carries a From line that
// names owner, the proof that submit stamped (and Export emitted) the sender
// identity rather than shipping a From-less message.
func hasFromOwner(raw []byte, owner string) bool {
	for line := range bytes.SplitSeq(raw, []byte("\r\n")) {
		if len(line) == 0 {
			break // end of header block
		}
		if bytes.HasPrefix(line, []byte("From:")) && bytes.Contains(line, []byte(owner)) {
			return true
		}
	}
	return false
}
