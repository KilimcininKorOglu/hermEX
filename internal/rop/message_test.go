package rop

import (
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// buildOpenMessage builds a RopOpenMessage request (OutputHandleIndex, Cpid,
// FolderId, OpenModeFlags, MessageId).
func buildOpenMessage(inIdx, outIdx uint8, folderEID, messageEID uint64) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropOpenMessage)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint16(0) // Cpid
	b.Uint64(folderEID)
	b.Uint8(0) // OpenModeFlags
	b.Uint64(messageEID)
	return b.Bytes()
}

// buildGetProps builds a RopGetPropertiesSpecific (cols != nil) or
// RopGetPropertiesAll (cols == nil) request.
func buildGetProps(ropID, inIdx uint8, cols []mapi.PropTag) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropID)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint16(0) // PropertySizeLimit
	b.Uint16(1) // WantUnicode
	if ropID == ropGetPropertiesSpecific {
		mustBuild(b.PropTags(cols))
	}
	return b.Bytes()
}

// pullTypedString reads a TYPED_STRING the way ropOpenMessage writes one.
func pullTypedString(t *testing.T, p *ext.Pull) string {
	t.Helper()
	switch typ := mustU8(t, p, "stringType"); typ {
	case 0x0, stringTypeEmpty: // NONE / EMPTY (both carry no body)
		return ""
	case stringTypeUnicode:
		s, err := p.Unicode()
		if err != nil {
			t.Fatalf("typed string (unicode): %v", err)
		}
		return s
	default: // STRING8 / UNICODE_REDUCED
		s, err := p.String8()
		if err != nil {
			t.Fatalf("typed string (string8): %v", err)
		}
		return s
	}
}

// seedInboxMessage delivers one message into the mailbox's Inbox and returns its
// objectstore id.
func seedInboxMessage(t *testing.T, dir, subject string) int64 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	raw := []byte("From: sender@hermex.test\r\nTo: alice@hermex.test\r\n" +
		"Subject: " + subject + "\r\nDate: Mon, 01 Jan 2024 10:00:00 +0000\r\n\r\nhello body\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// TestOpenMessageAndGetProps seeds a message, opens it off the logon, and reads
// it back two ways: the OpenMessage subject, GetPropertiesSpecific (a single
// PROPERTY_ROW), and GetPropertiesAll (the full TPROPVAL_ARRAY).
func TestOpenMessageAndGetProps(t *testing.T) {
	dir := provisionedDir(t)
	msgID := seedInboxMessage(t, dir, "READMSG")
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(msgID)))
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]

	// OpenMessage off the logon: input slot 0, message output slot 1.
	om, h := sess.Dispatch(buildOpenMessage(0, 1, inboxEID, msgEID), []uint32{logonH, 0xFFFFFFFF})
	p := ropOK(t, om, ropOpenMessage, "OpenMessage")
	assertOpenMessageBody(t, p, "READMSG")
	msgH := h[1]
	if obj := sess.get(msgH); obj == nil || obj.kind != kindMessage || obj.messageID != msgID {
		t.Fatalf("message object wrong: %+v", obj)
	}

	// GetPropertiesSpecific: a single PROPERTY_ROW over PrSubject.
	cols := []mapi.PropTag{mapi.PrSubject}
	gps := dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), msgH)
	p = ropOK(t, gps, ropGetPropertiesSpecific, "GetPropertiesSpecific")
	wantProp(t, decodeRow(t, p, cols), mapi.PrSubject, "READMSG", "GetPropertiesSpecific subject")

	// GetPropertiesAll: the full property bag as a TPROPVAL_ARRAY.
	gpa := dispatchOn(sess, buildGetProps(ropGetPropertiesAll, 0, nil), msgH)
	p = ropOK(t, gpa, ropGetPropertiesAll, "GetPropertiesAll")
	all, err := p.PropertyValues()
	if err != nil {
		t.Fatalf("decode TPROPVAL_ARRAY: %v", err)
	}
	wantProp(t, all, mapi.PrSubject, "READMSG", "GetPropertiesAll subject")
}

// assertOpenMessageBody checks the RopOpenMessage response body: the named-
// property flag, the split subject, and the single-recipient table that follows
// it.
func assertOpenMessageBody(t *testing.T, p *ext.Pull, subject string) {
	t.Helper()
	mustU8(t, p, "hasNamedProperties")
	pullTypedString(t, p) // SubjectPrefix (none for an unprefixed subject)
	if got := pullTypedString(t, p); got != subject {
		t.Errorf("NormalizedSubject = %q, want %q", got, subject)
	}
	wantU16(t, p, "RecipientCount (the To recipient)", 1)
	rcols, err := p.PropTags() // RecipientColumns (empty)
	if err != nil {
		t.Fatalf("RecipientColumns: %v", err)
	}
	wantU8(t, p, "recipient RowCount", 1)
	wantU8(t, p, "RecipientType (To)", uint8(mapi.RecipTo))
	rbag := pullOpenRecipientRest(t, p, rcols, "recipient")
	if got := stringProp(rbag, mapi.PrEmailAddress); got != "alice@hermex.test" {
		t.Errorf("recipient email = %q, want alice@hermex.test", got)
	}
}

// TestReadReflectsBufferedEdits locks MAPI's read-your-writes contract on an
// opened message: a SetProperties and a DeleteProperties issued before any
// SaveChangesMessage are visible to a GetProperties on the same handle, while the
// store stays untouched until the save. Without the overlay a read would return
// the stale persisted values.
func TestReadReflectsBufferedEdits(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "RYW"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]
	store := sess.get(logonH).store

	h = dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)
	msgH := h[1]

	// Buffer a set (importance -> 2, distinct from the seed's normal/1) and a delete
	// (subject) without saving.
	ropOK(t, dispatchOn(sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrImportance, Value: int32(2)}}), msgH), ropSetProperties, "SetProperties")
	ropOK(t, dispatchOn(sess, buildDeletePropsOp(ropDeleteProperties, 0, []mapi.PropTag{mapi.PrSubject}), msgH), ropDeleteProperties, "DeleteProperties")

	// GetPropertiesSpecific reflects the working copy: importance is the set value,
	// subject is absent (the buffered delete).
	cols := []mapi.PropTag{mapi.PrImportance, mapi.PrSubject}
	p := ropOK(t, dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), msgH), ropGetPropertiesSpecific, "GetPropertiesSpecific")
	row := decodeRow(t, p, cols)
	wantProp(t, row, mapi.PrImportance, int32(2), "buffered importance")
	if v, ok := row.Get(mapi.PrSubject); ok {
		t.Errorf("read still returned the subject (%v) after a buffered delete", v)
	}

	// The store is untouched until SaveChangesMessage: the persisted subject is
	// still there and importance is not the buffered value.
	stored := mustMessageProps(t, store, int64(mid), mapi.PrSubject, mapi.PrImportance)
	wantProp(t, stored, mapi.PrSubject, "RYW", "persisted subject")
	if v, ok := stored.Get(mapi.PrImportance); ok && v == int32(2) {
		t.Error("the buffered set was persisted before SaveChangesMessage")
	}
}

// TestOpenMessageNotFound confirms opening a non-existent message id yields
// ecNotFound.
func TestOpenMessageNotFound(t *testing.T) {
	dir := provisionedDir(t)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]

	bogus := uint64(mapi.MakeEIDEx(1, 0x7FFFFF))
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	resp, outH := sess.Dispatch(buildOpenMessage(0, 1, inboxEID, bogus), []uint32{logonH, 0xFFFFFFFF})
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropOpenMessage {
		t.Fatalf("RopId = %#x, want OpenMessage", id)
	}
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecNotFound {
		t.Errorf("OpenMessage(bogus) ReturnValue = %#x, want ecNotFound (%#x)", ec, ecNotFound)
	}
	if outH[1] != 0xFFFFFFFF {
		t.Errorf("OpenMessage(bogus) allocated handle %#x, want 0xFFFFFFFF", outH[1])
	}
}

// TestBrowseOpenChain proves the end-to-end read path: browse a folder with
// PrMid in the column set, take the message id the row hands back, and
// OpenMessage it. PrMid is synthesized in the row projection, so this is the
// integration check that the browse->open chain actually closes.
func TestBrowseOpenChain(t *testing.T) {
	dir := provisionedDir(t)
	seedInboxMessage(t, dir, "CHAINMSG")
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]
	h = dispatchOK(t, sess, buildOpenFolder(0, 1, inboxEID), ropOpenFolder, "OpenFolder(Inbox)", logonH, 0xFFFFFFFF)
	folderH := h[1]
	h = dispatchOK(t, sess, buildGetContentsTable(0, 1), ropGetContentsTable, "GetContentsTable", folderH, 0xFFFFFFFF)
	tableH := h[1]

	cols := []mapi.PropTag{mapi.PrMid, mapi.PrSubject}
	ropOK(t, dispatchOn(sess, buildSetColumns(0, cols), tableH), ropSetColumns, "SetColumns")
	qr := dispatchOn(sess, buildQueryRows(0, 0, 1, 32), tableH)
	origin, rows := queryRowsResponse(t, qr, cols)
	if origin != bookmarkEnd {
		t.Errorf("QueryRows Origin = %d, want END (%d)", origin, bookmarkEnd)
	}
	if len(rows) != 1 {
		t.Fatalf("browse returned %d rows, want 1", len(rows))
	}
	midVal, ok := rows[0].Get(mapi.PrMid)
	if !ok {
		t.Fatal("row has no PrMid, the browse->open chain is broken (no id to open)")
	}
	mid, ok := midVal.(int64)
	if !ok {
		t.Fatalf("PrMid value type = %T, want int64", midVal)
	}

	// Open the message by exactly the id the browse handed back.
	p := ropOK(t, dispatchOn(sess, buildOpenMessage(0, 1, inboxEID, uint64(mid)), logonH, 0xFFFFFFFF), ropOpenMessage, "OpenMessage(browsed PrMid)")
	mustU8(t, p, "hasNamedProperties")
	pullTypedString(t, p) // SubjectPrefix
	if got := pullTypedString(t, p); got != "CHAINMSG" {
		t.Errorf("opened-message subject = %q, want \"CHAINMSG\"", got)
	}
}
