package rop

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// markRead marks a stored message read the way another protocol does, through the
// store's read state rather than a MAPI property write.
func markRead(t *testing.T, dir string, id int64) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetMessageReadState(id, true); err != nil {
		t.Fatal(err)
	}
}

// TestMessageFlagsReachAMAPIClient proves an opened message and a contents table
// row report PidTagMessageFlags, PidTagRead and PidTagHasAttachments as the store
// computes them ([MS-OXCMSG] 2.2.1.6), so a message read on another protocol
// shows read in Outlook, and an unread-only view filters on the read bit.
func TestMessageFlagsReachAMAPIClient(t *testing.T) {
	dir := t.TempDir()
	readID := seedInboxMessage(t, dir, "Seen")
	seedInboxMessage(t, dir, "Unseen")
	markRead(t, dir, readID)

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(readID)))
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, msgEID), []uint32{h[0], 0xFFFFFFFF})
	cols := []mapi.PropTag{mapi.PrMessageFlags, mapi.PrRead, mapi.PrHasAttachments}
	resp, _ := sess.Dispatch(buildGetProps(ropGetPropertiesSpecific, 0, cols), []uint32{h[1]})
	row := decodeRow(t, ropOK(t, resp, ropGetPropertiesSpecific, "GetPropertiesSpecific"), cols)
	wantProp(t, row, mapi.PrMessageFlags, int32(mapi.MsgFlagRead), "message flags")
	wantProp(t, row, mapi.PrRead, true, "PidTagRead")
	wantProp(t, row, mapi.PrHasAttachments, false, "PidTagHasAttachments")

	tableH := openInboxContentsTable(t, sess)
	tcols := []mapi.PropTag{mapi.PrSubject, mapi.PrMessageFlags}
	mustDispatchOK(t, sess, buildSetColumns(0, tcols), []uint32{tableH}, ropSetColumns)
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, tcols)
	for _, r := range rows {
		subject, _ := r.Get(mapi.PrSubject)
		want := int32(0)
		if subject == "Seen" {
			want = mapi.MsgFlagRead
		}
		wantProp(t, r, mapi.PrMessageFlags, want, "row flags of "+subject.(string))
	}

	unread := &mapi.Restriction{Type: mapi.ResBitmask, Value: mapi.BitmaskRestriction{
		Relop: mapi.BmrEqz, PropTag: mapi.PrMessageFlags, Mask: mapi.MsgFlagRead,
	}}
	mustDispatchOK(t, sess, buildRestrict(0, unread), []uint32{tableH}, ropRestrict)
	qr, _ = sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows = queryRowsResponse(t, qr, tcols)
	assertSubjects(t, rows, "Unseen")
}
