package activesync

import (
	"strconv"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

func deletedID() string { return strconv.FormatInt(int64(mapi.PrivateFIDDeletedItems), 10) }

func moveReq(srcMsg, srcFld, dstFld string) *wbxml.Node {
	return wbxml.Elem(wbxml.MOMoves,
		wbxml.Elem(wbxml.MOMove,
			wbxml.Str(wbxml.MOSrcMsgId, srcMsg),
			wbxml.Str(wbxml.MOSrcFldId, srcFld),
			wbxml.Str(wbxml.MODstFldId, dstFld)))
}

// TestMoveItems confirms a message moves to the destination folder, the response
// reports success with the new id, and the store reflects the move.
func TestMoveItems(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	sid := firstInboxServerID(t, ts)

	_, root := postCommand(t, ts, "MoveItems", moveReq(sid, inboxID(), deletedID()))
	resp := root.Child(wbxml.MOResponse)
	if resp == nil || resp.ChildText(wbxml.MOStatus) != "3" {
		t.Fatalf("move Status = %q, want 3 (success)", resp.ChildText(wbxml.MOStatus))
	}
	dstMsg := resp.ChildText(wbxml.MODstMsgId)
	if dstMsg == "" {
		t.Fatal("move did not return a destination id")
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dstUID, _ := strconv.ParseUint(dstMsg, 10, 32)
	if _, err := st.MessageByUID(int64(mapi.PrivateFIDDeletedItems), uint32(dstUID)); err != nil {
		t.Errorf("moved message not found in Deleted Items: %v", err)
	}
	srcUID, _ := strconv.ParseUint(sid, 10, 32)
	if _, err := st.MessageByUID(int64(mapi.PrivateFIDInbox), uint32(srcUID)); err == nil {
		t.Error("source message still present in Inbox after move")
	}
}

// TestMoveItemsSameFolder confirms a move whose source and destination match
// reports the same-source-and-destination status.
func TestMoveItemsSameFolder(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	sid := firstInboxServerID(t, ts)
	_, root := postCommand(t, ts, "MoveItems", moveReq(sid, inboxID(), inboxID()))
	if s := root.Child(wbxml.MOResponse).ChildText(wbxml.MOStatus); s != "4" {
		t.Errorf("same-folder move Status = %q, want 4", s)
	}
}

// TestMoveItemsInvalidSource confirms a move of an unknown message reports an
// invalid-source status.
func TestMoveItemsInvalidSource(t *testing.T) {
	ts, _ := seededServer(t)
	_, root := postCommand(t, ts, "MoveItems", moveReq("999999", inboxID(), deletedID()))
	if s := root.Child(wbxml.MOResponse).ChildText(wbxml.MOStatus); s != "1" {
		t.Errorf("invalid-source move Status = %q, want 1", s)
	}
}
