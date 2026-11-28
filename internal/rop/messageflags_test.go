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
	defer mustClose(t, st)
	if err := st.SetMessageReadState(id, true); err != nil {
		t.Fatal(err)
	}
}

// TestMessageFlagsReachAMAPIClient proves an opened message and a contents table
// row report PidTagMessageFlags, PidTagRead and PidTagHasAttachments as the store
// computes them ([MS-OXCMSG] 2.2.1.6), so a message read on another protocol
// shows read in Outlook, and an unread-only view filters on the read bit.
func TestMessageFlagsReachAMAPIClient(t *testing.T) {
	dir := provisionedDir(t)
	readID := seedInboxMessage(t, dir, "Seen")
	seedInboxMessage(t, dir, "Unseen")
	markRead(t, dir, readID)

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(readID)))
	h = dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, msgEID), ropOpenMessage, "OpenMessage", h[0], 0xFFFFFFFF)
	cols := []mapi.PropTag{mapi.PrMessageFlags, mapi.PrRead, mapi.PrHasAttachments}
	resp := dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), h[1])
	row := decodeRow(t, ropOK(t, resp, ropGetPropertiesSpecific, "GetPropertiesSpecific"), cols)
	wantProp(t, row, mapi.PrMessageFlags, int32(mapi.MsgFlagRead), "message flags")
	wantProp(t, row, mapi.PrRead, true, "PidTagRead")
	wantProp(t, row, mapi.PrHasAttachments, false, "PidTagHasAttachments")

	tableH := openInboxContentsTable(t, sess)
	tcols := []mapi.PropTag{mapi.PrSubject, mapi.PrMessageFlags}
	mustDispatchOK(t, sess, buildSetColumns(0, tcols), []uint32{tableH}, ropSetColumns)
	qr := dispatchOn(sess, buildQueryRows(0, 0, 1, 32), tableH)
	origin, rows := queryRowsResponse(t, qr, tcols)
	if origin != bookmarkEnd {
		t.Errorf("QueryRows Origin = %d, want END (%d)", origin, bookmarkEnd)
	}
	if len(rows) != 2 {
		t.Fatalf("contents table rows = %d, want the 2 seeded messages", len(rows))
	}
	for _, r := range rows {
		subVal, ok := r.Get(mapi.PrSubject)
		if !ok {
			t.Fatalf("row missing PrSubject: %v", r)
		}
		subject, ok := subVal.(string)
		if !ok {
			t.Fatalf("PrSubject type = %T, want string", subVal)
		}
		want := int32(0)
		if subject == "Seen" {
			want = mapi.MsgFlagRead
		}
		wantProp(t, r, mapi.PrMessageFlags, want, "row flags of "+subject)
	}

	unread := &mapi.Restriction{Type: mapi.ResBitmask, Value: mapi.BitmaskRestriction{
		Relop: mapi.BmrEqz, PropTag: mapi.PrMessageFlags, Mask: mapi.MsgFlagRead,
	}}
	mustDispatchOK(t, sess, buildRestrict(0, unread), []uint32{tableH}, ropRestrict)
	qr = dispatchOn(sess, buildQueryRows(0, 0, 1, 32), tableH)
	origin, rows = queryRowsResponse(t, qr, tcols)
	if origin != bookmarkEnd {
		t.Errorf("restricted QueryRows Origin = %d, want END (%d)", origin, bookmarkEnd)
	}
	assertSubjects(t, rows, "Unseen")
}

// TestReviewerCannotFlipMessageReadFlag proves a delegate holding only Reviewer
// rights cannot alter a message's read flag: SetMessageReadFlag answers
// ecAccessDenied and the stored flags stay unread.
func TestReviewerCannotFlipMessageReadFlag(t *testing.T) {
	dir := provisionedDir(t)
	unseenID := seedInboxMessage(t, dir, "Unseen")
	const delegate = "delegate@hermex.test"
	grantFolderPermission(t, dir, int64(mapi.PrivateFIDInbox), delegate, mapi.RightsReviewer)

	sess, logonH := delegateLogon(t, dir, delegate)
	defer sess.Close()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(unseenID)))
	h := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, msgEID), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)

	resp := dispatchOn(sess, buildSetMessageReadFlag(0, 1, rfDefault), logonH, h[1])
	if ec := readEC(t, resp, ropSetMessageReadFlag); ec != ecAccessDenied {
		t.Errorf("Reviewer SetMessageReadFlag ec = %#x, want ecAccessDenied (%#x)", ec, ecAccessDenied)
	}
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), unseenID); f != 0 {
		t.Errorf("stored flags after refused read-flag flip = %#x, want 0 (unread)", f)
	}
}
