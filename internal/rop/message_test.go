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
		_ = b.PropTags(cols)
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
	defer st.Close()
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
	dir := t.TempDir()
	msgID := seedInboxMessage(t, dir, "READMSG")
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(msgID)))
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	// OpenMessage off the logon: input slot 0, message output slot 1.
	om, h := sess.Dispatch(buildOpenMessage(0, 1, inboxEID, msgEID), []uint32{logonH, 0xFFFFFFFF})
	p := ext.NewPull(om, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropOpenMessage {
		t.Fatalf("OpenMessage RopId = %#x", id)
	}
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("OpenMessage ReturnValue = %#x", ec)
	}
	mustU8(t, p, "hasNamedProperties")
	pullTypedString(t, p) // SubjectPrefix (none for an unprefixed subject)
	if got := pullTypedString(t, p); got != "READMSG" {
		t.Errorf("NormalizedSubject = %q, want \"READMSG\"", got)
	}
	if rc := mustU16(t, p, "recipientCount"); rc != 0 {
		t.Errorf("RecipientCount = %d, want 0 (inline recipient table deferred)", rc)
	}
	if _, err := p.PropTags(); err != nil { // RecipientColumns (empty)
		t.Fatalf("RecipientColumns: %v", err)
	}
	if rows := mustU8(t, p, "rowCount"); rows != 0 {
		t.Errorf("recipient RowCount = %d, want 0", rows)
	}
	msgH := h[1]
	if obj := sess.get(msgH); obj == nil || obj.kind != kindMessage || obj.messageID != msgID {
		t.Fatalf("message object wrong: %+v", obj)
	}

	// GetPropertiesSpecific: a single PROPERTY_ROW over PrSubject.
	cols := []mapi.PropTag{mapi.PrSubject}
	gps, _ := sess.Dispatch(buildGetProps(ropGetPropertiesSpecific, 0, cols), []uint32{msgH})
	p = ext.NewPull(gps, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropGetPropertiesSpecific {
		t.Fatalf("GetPropertiesSpecific RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("GetPropertiesSpecific ReturnValue = %#x", ec)
	}
	row := decodeRow(t, p, cols)
	if subj, _ := row.Get(mapi.PrSubject); subj != "READMSG" {
		t.Errorf("GetPropertiesSpecific subject = %v, want \"READMSG\"", subj)
	}

	// GetPropertiesAll: the full property bag as a TPROPVAL_ARRAY.
	gpa, _ := sess.Dispatch(buildGetProps(ropGetPropertiesAll, 0, nil), []uint32{msgH})
	p = ext.NewPull(gpa, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropGetPropertiesAll {
		t.Fatalf("GetPropertiesAll RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("GetPropertiesAll ReturnValue = %#x", ec)
	}
	all, err := p.PropertyValues()
	if err != nil {
		t.Fatalf("decode TPROPVAL_ARRAY: %v", err)
	}
	if subj, ok := all.Get(mapi.PrSubject); !ok || subj != "READMSG" {
		t.Errorf("GetPropertiesAll missing/wrong subject: %v (present=%v)", subj, ok)
	}
}

// TestOpenMessageNotFound confirms opening a non-existent message id yields
// ecNotFound.
func TestOpenMessageNotFound(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	bogus := uint64(mapi.MakeEIDEx(1, 0x7FFFFF))
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	resp, _ := sess.Dispatch(buildOpenMessage(0, 1, inboxEID, bogus), []uint32{logonH, 0xFFFFFFFF})
	p := ext.NewPull(resp, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecNotFound {
		t.Errorf("OpenMessage(bogus) ReturnValue = %#x, want ecNotFound (%#x)", ec, ecNotFound)
	}
}

// TestBrowseOpenChain proves the end-to-end read path: browse a folder with
// PrMid in the column set, take the message id the row hands back, and
// OpenMessage it. PrMid is synthesized in the row projection, so this is the
// integration check that the browse->open chain actually closes.
func TestBrowseOpenChain(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "CHAINMSG")
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]
	_, h = sess.Dispatch(buildGetContentsTable(0, 1), []uint32{folderH, 0xFFFFFFFF})
	tableH := h[1]

	cols := []mapi.PropTag{mapi.PrMid, mapi.PrSubject}
	sess.Dispatch(buildSetColumns(0, cols), []uint32{tableH})
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	if len(rows) != 1 {
		t.Fatalf("browse returned %d rows, want 1", len(rows))
	}
	midVal, ok := rows[0].Get(mapi.PrMid)
	if !ok {
		t.Fatal("row has no PrMid — the browse->open chain is broken (no id to open)")
	}
	mid, ok := midVal.(int64)
	if !ok {
		t.Fatalf("PrMid value type = %T, want int64", midVal)
	}

	// Open the message by exactly the id the browse handed back.
	om, _ := sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mid)), []uint32{logonH, 0xFFFFFFFF})
	p := ext.NewPull(om, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("OpenMessage via browsed PrMid: ec = %#x (chain broken)", ec)
	}
	mustU8(t, p, "hasNamedProperties")
	pullTypedString(t, p) // SubjectPrefix
	if got := pullTypedString(t, p); got != "CHAINMSG" {
		t.Errorf("opened-message subject = %q, want \"CHAINMSG\"", got)
	}
}
